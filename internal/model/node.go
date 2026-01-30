package model

import (
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// NodePool 节点池表
type NodePool struct {
	UUID   string            `gorm:"primaryKey;column:uuid;type:varchar(36)" json:"uuid"` // 节点池唯一标识符
	Name   string            `gorm:"column:name" json:"name"`                             // 节点池名称
	Links  map[string]string `gorm:"column:links;serializer:json" json:"links"`           // 节点池相关链接
	Region string            `gorm:"column:region" json:"region"`                         // 存储国家信息
	Remark string            `gorm:"column:remark" json:"remark"`                         // 备注信息
}

func (NodePool) TableName() string {
	return "node_pool"
}

// Subscription 订阅表结构 (用于 sub_0, sub_1 等动态表)
type Subscription struct {
	UUID        string `gorm:"primaryKey;type:varchar(36)" json:"uuid"`                                                                // 链接唯一标识符
	Name        string `gorm:"column:name" json:"name"`                                                                                // 在订阅中显示的节点名称
	NodeUUID    string `gorm:"column:node_uuid;type:varchar(36);index;constraint:OnUpdate:CASCADE,OnDelete:CASCADE;" json:"node_uuid"` // 关联的节点 UUID
	LinkType    string `gorm:"column:link_type;type:varchar(20)" json:"link_type"`                                                     // 链接类型，如 "hy2", "socks5", "ss", "tuic", "vless"
	RoutingType int    `gorm:"column:routing_type;default:0" json:"routing_type"`                                                      // 路由类型
	SortIndex   int    `gorm:"column:sort_index;default:0" json:"sort_index"`                                                          // 排序索引
}

// 在创建记录前自动生成 UUID
func (s *Subscription) BeforeCreate(tx *gorm.DB) (err error) {
	if s.UUID == "" {
		s.UUID = uuid.New().String()
	}
	return
}
