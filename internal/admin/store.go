package admin

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// AdminConfig admin.json 结构。
type AdminConfig struct {
	PasswordHash  string `json:"password_hash"`
	SessionSecret string `json:"session_secret"`
	Enabled       bool   `json:"enabled"`
}

// LoadAdmin 读取 admin.json。文件不存在返回包装的 os.ErrNotExist。
func LoadAdmin(path string) (AdminConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return AdminConfig{}, fmt.Errorf("读取 admin.json 失败: %w", err)
	}
	var ac AdminConfig
	if err := json.Unmarshal(data, &ac); err != nil {
		return AdminConfig{}, fmt.Errorf("解析 admin.json 失败: %w", err)
	}
	return ac, nil
}

// SaveAdmin 原子写 admin.json（临时文件 + rename，chmod 0600）。
func SaveAdmin(path string, ac AdminConfig) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return fmt.Errorf("创建 admin 目录失败: %w", err)
	}
	data, err := json.MarshalIndent(ac, "", "  ")
	if err != nil {
		return fmt.Errorf("序列化 admin.json 失败: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".admin-*.json")
	if err != nil {
		return fmt.Errorf("创建临时文件失败: %w", err)
	}
	tmpPath := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return fmt.Errorf("写入临时文件失败: %w", err)
	}
	tmp.Close()
	if err := os.Chmod(tmpPath, 0600); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("设置权限失败: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("原子替换 admin.json 失败: %w", err)
	}
	return nil
}
