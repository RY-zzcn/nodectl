package model

// SystemConfig 系统配置表
type SystemConfig struct {
	Key    string `gorm:"primaryKey;column:key;type:varchar(50)" json:"key"` // 配置项键
	Value  string `gorm:"column:value;type:text" json:"value"`               // 配置项值
	Remark string `gorm:"column:remark;type:varchar(255)" json:"remark"`     // 备注信息
}

// TableName 自定义表名为 sys_config
func (SystemConfig) TableName() string {
	return "sys_config"
}
