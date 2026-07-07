// Package config 配置加载与原子写入
package config

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// Load 从 YAML 字节加载配置，返回校验通过的快照
func Load(data []byte) (ConfigSnapshot, error) {
	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return ConfigSnapshot{}, fmt.Errorf("yaml 解析失败: %w", err)
	}
	if err := Validate(cfg); err != nil {
		return ConfigSnapshot{}, fmt.Errorf("配置校验失败: %w", err)
	}
	return NewSnapshot(cfg), nil
}

// LoadFile 从文件路径加载配置
func LoadFile(path string) (ConfigSnapshot, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return ConfigSnapshot{}, fmt.Errorf("读取配置文件 %s 失败: %w", path, err)
	}
	return Load(data)
}

// Save 原子写入配置到文件（写临时文件 → rename）
func Save(cfg Config, path string) error {
	data, err := yaml.Marshal(&cfg)
	if err != nil {
		return fmt.Errorf("序列化配置失败: %w", err)
	}

	dir := filepath.Dir(path)
	tmpFile, err := os.CreateTemp(dir, ".config-*.yaml")
	if err != nil {
		return fmt.Errorf("创建临时文件失败: %w", err)
	}
	tmpPath := tmpFile.Name()

	if _, err := tmpFile.Write(data); err != nil {
		tmpFile.Close()
		os.Remove(tmpPath)
		return fmt.Errorf("写入临时文件失败: %w", err)
	}
	tmpFile.Close()

	if err := os.Rename(tmpPath, path); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("原子替换配置文件失败: %w", err)
	}
	return nil
}

// EnsureConfig 确保配置文件存在，不存在则自动创建带有默认值的配置文件。
// 返回生成的私有 key（首次创建时）或空字符串。
func EnsureConfig(path string) (string, error) {
	if _, err := os.Stat(path); err == nil {
		return "", nil // 文件已存在，无需创建
	} else if !os.IsNotExist(err) {
		return "", fmt.Errorf("检查配置文件 %s 失败: %w", path, err)
	}

	// 创建配置目录
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", fmt.Errorf("创建配置目录 %s 失败: %w", dir, err)
	}

	// 生成随机私有 key
	privateKey, err := genPrivateKey()
	if err != nil {
		return "", fmt.Errorf("生成私有 key 失败: %w", err)
	}

	// 生成默认配置文件内容
	content := defaultConfigContent(privateKey)

	// 写入文件并设权限 0600
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		return "", fmt.Errorf("写入配置文件 %s 失败: %w", path, err)
	}

	return privateKey, nil
}

// genPrivateKey 生成 sk-cs- 前缀的随机私有 key
func genPrivateKey() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return "sk-cs-" + hex.EncodeToString(b), nil
}

// defaultConfigContent 返回带注释的默认配置 YAML 内容
func defaultConfigContent(privateKey string) string {
	return fmt.Sprintf(`# Claude Switch 配置文件
# 使用 "cs" 命令管理配置，或直接编辑此文件后重启 cs-proxy（自动热重载）
#
# 快速上手：
#   1. cs upstream add <name> --url ... --apikey ... --model ...   # 添加上游（--model 可多次指定）
#   2. cs mapping add default <请求模型名> <upstream/model>          # 配置模型路由
#   3. cs proxy start                                               # 启动代理
#
# 详细文档：https://github.com/cnstark/claude-switch

server:
  listen: 127.0.0.1:8787
  log_level: info
  # log_file: ""            # 自定义日志路径，空=默认 ~/.claude_switch/logs/cs-proxy.log
  # log_max_days: 7         # 日志保留天数，0=永久保留
  # 私有 key → 项目名 映射（已自动生成一个 key，也可用 cs key gen 生成新的）
  private_keys:
    %s: default

# 上游 API 池（至少需要配置一个可用的上游）
# 示例配置，请替换为你的实际 API 信息后取消注释：
# upstreams:
#   - name: anthropic
#     url: https://api.anthropic.com
#     apikey: sk-ant-your-api-key-here
#     timeout: 60s
#     models:
#       - claude-sonnet-4-6
#       - claude-opus-4-8

projects:
  - name: default
    log_level: off
    model_map: {}
    # allow_direct_access: false  # 开启后可用真实模型名直接访问（无需配别名）
    # 示例 model_map，添加上游后按如下格式配置（值为 upstream/model）：
    # model_map:
    #   my-alias:
    #     - anthropic/claude-sonnet-4-6
`, privateKey)
}
