#!/usr/bin/env python3
"""Offline regressions for static endpoint and query-name extraction."""
import unittest

from harvest_static import extract_endpoints


class ExtractEndpointsTests(unittest.TestCase):
    def assert_endpoint(self, source, path, name=None):
        api, other, params = extract_endpoints(source)
        self.assertIn(path, api | other)
        self.assertEqual(params, {f"{path}\t{name}"} if name else set())

    def test_double_quoted_query(self):
        self.assert_endpoint('api.get("/api/users/profile?userId=" + id)', "/api/users/profile", "userId")

    def test_single_quoted_query(self):
        self.assert_endpoint("url: '/rest/orders/list?page='+p", "/rest/orders/list", "page")

    def test_template_query(self):
        source = "fetch(" + chr(96) + "/api/search?q=" + chr(96) + " + term)"
        self.assert_endpoint(source, "/api/search", "q")

    def test_export_query_prefix(self):
        # Dynamic fragments beyond the first literal are not inferred.
        self.assert_endpoint("window.open('/service/report/export?from='+a+'&to='+b)", "/service/report/export", "from")

    def test_gateway_query(self):
        self.assert_endpoint('xhr.open("GET", "/gateway/v1/items?category=" + c)', "/gateway/v1/items", "category")

    def test_backend_query(self):
        self.assert_endpoint("var u = '/backend/metrics?scope='+s", "/backend/metrics", "scope")

    def test_query_free_control(self):
        self.assert_endpoint('api.get("/api/catalog/categories/list")', "/api/catalog/categories/list")

    def test_asset_control(self):
        self.assertEqual(extract_endpoints('<script src="/static/app/main.bundle.js">'), (set(), set(), set()))

    def test_all_literal_parameter_pairs(self):
        api, other, params = extract_endpoints('"/api/a?x=1&y=2"; "/rest/b?z=3"')
        self.assertEqual(api, {"/api/a", "/rest/b"})
        self.assertEqual(other, set())
        self.assertEqual(params, {"/api/a\tx", "/api/a\ty", "/rest/b\tz"})

    def test_ignored_paths_have_no_parameter_rows(self):
        for source in ['"/static/main.js?v=1"', '"/assets/icon.png?v=1"', '"/health?v=1"']:
            with self.subTest(source=source):
                self.assertEqual(extract_endpoints(source), (set(), set(), set()))

    def test_duplicate_paths_and_names_are_deduplicated(self):
        api, other, params = extract_endpoints('"/api/a?x=1"; "/api/a?x=2"; "/api/a"')
        self.assertEqual(api, {"/api/a"})
        self.assertEqual(other, set())
        self.assertEqual(params, {"/api/a\tx"})


if __name__ == "__main__":
    unittest.main()
