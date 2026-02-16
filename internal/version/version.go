package version

// Version 存储当前程序的版本号
// 默认值为 dev，在 GitHub Action 编译时会通过 -ldflags 动态覆盖此值
var Version = "dev"
