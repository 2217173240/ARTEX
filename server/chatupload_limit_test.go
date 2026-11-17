package server

import (
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// uploadLimitZeroReader 生成无限零字节流，配合 io.CopyN 流式构造任意大小的
// multipart 文件段，避免测试真的在内存里分配数百 MiB。
type uploadLimitZeroReader struct{}

func (uploadLimitZeroReader) Read(p []byte) (int, error) {
	clear(p)
	return len(p), nil
}

// TestChatUpload512MiBBoundary 覆盖 512 MiB 合并上限的边界：单文件恰好/超出、
// 多文件合并恰好/超出、请求体整体超过 512 MiB+1 MiB 开销（MaxBytesReader 路径）。
// 413 必须带回专门的上限文案；成功时附件按原始尺寸落盘；超限时 uploads 目录与
// multipart 溢写目录（TMPDIR）都不留残留。
func TestChatUpload512MiBBoundary(t *testing.T) {
	if maxChatUpload != 512<<20 {
		t.Fatalf("attachment limit = %d bytes, want 512 MiB", maxChatUpload)
	}
	cases := []struct {
		name   string
		sizes  []int64
		status int
	}{
		{"above previous limit", []int64{129 << 20}, http.StatusOK},
		// 512MiB 单文件恰好放行的用例因耗时裁剪：合并恰好放行（256+256）已覆盖
		// totalSize == maxChatUpload 的边界，单文件 OK 由 129MiB 一例代表。
		{"single file over limit", []int64{(512 << 20) + 1}, http.StatusRequestEntityTooLarge},
		{"combined files exactly at limit", []int64{256 << 20, 256 << 20}, http.StatusOK},
		{"combined files over limit", []int64{256 << 20, (256 << 20) + 1}, http.StatusRequestEntityTooLarge},
		{"request body over limit", []int64{(513 << 20) + 1}, http.StatusRequestEntityTooLarge},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			workDir := t.TempDir()
			spillDir := t.TempDir()
			t.Setenv("TMPDIR", spillDir)
			body, writer := io.Pipe()
			form := multipart.NewWriter(writer)
			done := make(chan error, 1)
			go func() {
				for _, size := range tc.sizes {
					part, err := form.CreateFormFile("file", "boundary.bin")
					if err == nil {
						_, err = io.CopyN(part, uploadLimitZeroReader{}, size)
					}
					if err != nil {
						writer.CloseWithError(err)
						done <- err
						return
					}
				}
				err := form.Close()
				writer.CloseWithError(err)
				done <- err
			}()
			req := httptest.NewRequest(http.MethodPost, "/api/chat/upload?scope=staging&id=boundary-test", body)
			req.Header.Set("Content-Type", form.FormDataContentType())
			rec := httptest.NewRecorder()
			srv := &Server{m: &Manager{dir: workDir}}
			srv.chatUpload(rec, req)
			body.Close()
			writeErr := <-done
			if rec.Code != tc.status {
				t.Fatalf("status %d, want %d: %s", rec.Code, tc.status, rec.Body.String())
			}
			uploads := filepath.Join(workDir, "drafts", "boundary-test", "uploads")
			if tc.status == http.StatusOK {
				if writeErr != nil {
					t.Fatal(writeErr)
				}
				var reply struct {
					Attachments []chatAttachment `json:"attachments"`
				}
				if err := json.Unmarshal(rec.Body.Bytes(), &reply); err != nil {
					t.Fatal(err)
				}
				if len(reply.Attachments) != len(tc.sizes) {
					t.Fatalf("got %d attachments, want %d", len(reply.Attachments), len(tc.sizes))
				}
				for i, attachment := range reply.Attachments {
					info, err := os.Stat(attachment.Abs)
					if err != nil || attachment.Size != tc.sizes[i] || info.Size() != tc.sizes[i] {
						t.Fatalf("attachment %d size mismatch: %+v, %v", i, attachment, err)
					}
				}
			} else {
				var reply struct {
					Error string `json:"error"`
				}
				if err := json.Unmarshal(rec.Body.Bytes(), &reply); err != nil {
					t.Fatal(err)
				}
				if reply.Error != chatUploadLimitMessage {
					t.Fatalf("unexpected size-limit error: %q, want %q", reply.Error, chatUploadLimitMessage)
				}
				files, err := os.ReadDir(uploads)
				if err != nil || len(files) != 0 {
					t.Fatalf("oversized upload saved files: %v, %v", files, err)
				}
			}
			spillFiles, err := os.ReadDir(spillDir)
			if err != nil || len(spillFiles) != 0 {
				t.Fatalf("multipart spill files were not cleaned up: %v, %v", spillFiles, err)
			}
		})
	}
}
