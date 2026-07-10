package admin

import (
	"path/filepath"
	"testing"
)

// TestNewServerMissingAdminJSONReturnsDisabled 验证 admin.json 不存在时，
// NewServer 返回 enabled=false 的 Server（而非 error）。
// 规格要求：文件不存在 → 后台对所有受保护路由返回 403，绝不裸奔。
// 关键：LoadAdmin 用 fmt.Errorf("%w") 包装 os.ReadFile 错误，os.IsNotExist
// 对这种再包装的 *fs.PathError 返回 false（Go 已知陷阱），必须用 errors.Is。
func TestNewServerMissingAdminJSONReturnsDisabled(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "does-not-exist.json")
	s, err := NewServer(missing)
	if err != nil {
		t.Fatalf("admin.json 不存在应返回 disabled Server 而非 error，得到: %v", err)
	}
	if s.enabled {
		t.Errorf("admin.json 不存在时 enabled 应为 false")
	}
	if s.sm != nil {
		t.Errorf("disabled Server 不应有 session manager")
	}
}
