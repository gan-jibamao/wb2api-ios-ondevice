//go:build !carchive

// Package main 为 c-archive 构建提供占位（真正的 C 入口见同目录 main.go，构建标签 carchive）。
package main

func main() {}
