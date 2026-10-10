import http.client
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import importlib.util
import io
from pathlib import Path
import socket
import threading
import unittest
from unittest.mock import Mock, patch
import urllib.error
import urllib.request

spec = importlib.util.spec_from_file_location('native_smoke', Path(__file__).with_name('native-smoke.py'))
module = importlib.util.module_from_spec(spec)
spec.loader.exec_module(module)


class BoundarySmokeTest(unittest.TestCase):
    url = 'http://127.0.0.1:1234/'
    headers = {'Host': 'attacker.invalid'}

    def http_error(self, code=403):
        body = io.BytesIO(b'forbidden')
        return urllib.error.HTTPError(self.url, code, 'fixture', {}, body), body

    def test_requires_actual_403_after_direct_or_wrapped_reset(self):
        for reset in (ConnectionResetError('fixture'),
                      urllib.error.URLError(ConnectionResetError('fixture')),
                      http.client.RemoteDisconnected('fixture')):
            with self.subTest(reset=type(reset).__name__):
                forbidden, body = self.http_error()
                opener = Mock()
                opener.open.side_effect = [reset, forbidden]
                with patch.object(module.time, 'sleep') as sleep:
                    module.expect_forbidden(opener, self.url, self.headers)
                self.assertEqual(opener.open.call_count, 2)
                sleep.assert_called_once_with(.1)
                self.assertTrue(body.closed)
                for call in opener.open.call_args_list:
                    request = call.args[0]
                    self.assertEqual(request.get_method(), 'POST')
                    self.assertEqual(request.get_header('Host'), 'attacker.invalid')
                    self.assertEqual(request.data, b'dsn=invalid')
                    self.assertEqual(call.kwargs, {'timeout': 5})

    def test_repeated_reset_fails_after_three_attempts(self):
        reset = ConnectionResetError('fixture')
        opener = Mock()
        opener.open.side_effect = reset
        with patch.object(module.time, 'sleep') as sleep:
            with self.assertRaisesRegex(RuntimeError, 'Host.*3 connection resets') as result:
                module.expect_forbidden(opener, self.url, self.headers)
        self.assertIs(result.exception.__cause__, reset)
        self.assertEqual(opener.open.call_count, 3)
        self.assertEqual(sleep.call_count, 2)

    def test_http_success_fails_without_retry_and_closes_response(self):
        response = io.BytesIO(b'accepted')
        opener = Mock()
        opener.open.return_value = response
        with self.assertRaisesRegex(AssertionError, 'Host boundary accepted'):
            module.expect_forbidden(opener, self.url, self.headers)
        opener.open.assert_called_once()
        self.assertTrue(response.closed)

    def test_wrong_http_error_fails_without_retry_and_closes_response(self):
        error, body = self.http_error(500)
        opener = Mock()
        opener.open.side_effect = error
        with self.assertRaisesRegex(AssertionError, 'HTTP 500, expected 403'):
            module.expect_forbidden(opener, self.url, {'Origin': 'https://attacker.invalid'})
        opener.open.assert_called_once()
        self.assertTrue(body.closed)

    def test_other_transport_and_protocol_failures_are_not_retried(self):
        for error in (urllib.error.URLError(ConnectionRefusedError('fixture')),
                      TimeoutError('fixture'), http.client.BadStatusLine('bad fixture')):
            with self.subTest(error=type(error).__name__):
                opener = Mock()
                opener.open.side_effect = error
                with self.assertRaises(type(error)) as result:
                    module.expect_forbidden(opener, self.url, self.headers)
                self.assertIs(result.exception, error)
                opener.open.assert_called_once()

    def test_real_loopback_disconnect_then_403_checks_both_boundaries(self):
        requests = []

        class Handler(BaseHTTPRequestHandler):
            def do_POST(self):
                self.rfile.read(int(self.headers['Content-Length']))
                headers = (self.headers.get('Host'), self.headers.get('Origin'))
                requests.append(headers)
                if len(requests) in (1, 3):
                    self.connection.shutdown(socket.SHUT_RDWR)
                    self.connection.close()
                    return
                self.send_response(403)
                self.end_headers()
                self.wfile.write(b'forbidden')

            def log_message(self, *_args):
                pass

        with ThreadingHTTPServer(('127.0.0.1', 0), Handler) as server:
            thread = threading.Thread(target=server.serve_forever, daemon=True)
            thread.start()
            try:
                url = f'http://127.0.0.1:{server.server_port}/'
                opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))
                module.expect_forbidden(opener, url, self.headers)
                module.expect_forbidden(opener, url, {'Origin': 'https://attacker.invalid'})
                self.assertEqual(requests, [
                    ('attacker.invalid', None), ('attacker.invalid', None),
                    (f'127.0.0.1:{server.server_port}', 'https://attacker.invalid'),
                    (f'127.0.0.1:{server.server_port}', 'https://attacker.invalid'),
                ])
            finally:
                server.shutdown()
                thread.join(timeout=5)
                self.assertFalse(thread.is_alive())


if __name__ == '__main__':
    unittest.main()
