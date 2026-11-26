//go:build doolla_bigquery

package drivers

// BigQuery 的 REQUIRED 标量不能为空，NULLABLE 可为空，REPEATED 表示数组。
// 可选驱动固定到首个支持 REQUIRED 原生解码的上游提交 ce985cbbc860；
// 无需改写查询响应的 schema，database/sql 也能保留原始列的可空性。
import _ "github.com/viant/bigquery"
