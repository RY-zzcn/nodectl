package service

import (
	"fmt"
	"nodectl/internal/database"
	"strings"

	"gopkg.in/yaml.v3"
)

// ClashProvider 是用于生成 0.yaml / 1.yaml 的根结构
type ClashProvider struct {
	Proxies []map[string]interface{} `yaml:"proxies"`
}

// GenerateRawNodesYAML 动态生成指定路由类型的节点 YAML
// routingType: 1=直连, 2=落地
func GenerateRawNodesYAML(routingType int, useFlag bool) (string, error) {
	var nodes []database.NodePool
	// 按照 SortIndex 排序获取节点
	if err := database.DB.Where("routing_type = ? AND is_blocked = ?", routingType, false).
		Order("sort_index ASC").Find(&nodes).Error; err != nil {
		return "", err
	}

	var proxyList []map[string]interface{}

	for _, node := range nodes {
		for proto, link := range node.Links {
			// 如果该协议被禁用，跳过
			if contains(node.DisabledLinks, proto) {
				continue
			}

			// 构造统一的前缀名 (例如 ss-香港节点)
			baseName := fmt.Sprintf("%s-%s", strings.ToLower(proto), node.Name)

			// 调用链接解析器
			proxyDict := ParseProxyLink(link, baseName, node.Region, useFlag)
			if proxyDict != nil {
				proxyList = append(proxyList, proxyDict)
			}
		}
	}

	// 包装进 Proxies 结构
	provider := ClashProvider{Proxies: proxyList}

	// 序列化为 YAML
	yamlBytes, err := yaml.Marshal(&provider)
	if err != nil {
		return "", err
	}

	return string(yamlBytes), nil
}

// 辅助函数：检查切片是否包含某个元素
func contains(slice []string, item string) bool {
	for _, s := range slice {
		if s == item {
			return true
		}
	}
	return false
}
