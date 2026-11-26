package database

import (
	"errors"
	"regexp"
	"strconv"
	"strings"
)

// ODBC braces protect semicolons. A literal closing brace is doubled.
var odbcField = regexp.MustCompile(`^\s*([^;={}]+?)\s*=\s*(?:\{((?:[^}]|\}\})*)\}\s*|("(?:[^"]|"")*"|[^;{][^;]*|))\s*(?:;|$)`)
var odbcDriverKey = regexp.MustCompile(`(?i)(?:^|;)\s*driver\s*=`)

// The native SQL Server driver's ADO parser handles UID/PWD aliases and
// Server=tcp:host,port. Quote each ODBC value for that parser individually.
func sqlServerDSN(dsn string) (string, error) {
	raw := strings.TrimSpace(dsn)
	explicit := len(raw) >= 5 && strings.EqualFold(raw[:5], "odbc:")
	if explicit {
		raw = raw[5:]
	} else if !odbcDriverKey.MatchString(raw) || strings.HasPrefix(raw, "sqlserver://") {
		return dsn, nil
	}
	var fields []string
	hasDriver := false
	hasEncrypt := false
	driverMajor := 0
	usesAuthentication := false
	for {
		raw = strings.TrimLeft(raw, " \r\n\t;")
		if raw == "" {
			break
		}
		m := odbcField.FindStringSubmatchIndex(raw)
		if m == nil {
			key, _, _ := strings.Cut(raw, "=")
			if !explicit && !hasDriver && !strings.EqualFold(strings.TrimSpace(key), "driver") {
				return dsn, nil
			}
			return "", errors.New("invalid SQL Server ODBC connection attribute")
		}
		key := strings.ToLower(strings.TrimSpace(raw[m[2]:m[3]]))
		var value string
		if m[4] >= 0 {
			value = strings.ReplaceAll(raw[m[4]:m[5]], "}}", "}")
		} else {
			value = strings.TrimSpace(raw[m[6]:m[7]])
		}
		raw = raw[m[1]:]
		switch key {
		case "driver":
			hasDriver = true
			name := strings.ToLower(value)
			if version, found := strings.CutPrefix(name, "odbc driver "); found {
				version, found = strings.CutSuffix(version, " for sql server")
				if found {
					driverMajor, _ = strconv.Atoi(version)
				}
			}
			continue
		case "encrypt":
			hasEncrypt = true
		case "authentication":
			usesAuthentication = value != "" && !strings.EqualFold(value, "none")
		case "trustservercertificate", "multisubnetfailover", "columnencryption", "disableretry", "notraceid", "guid conversion":
			if strings.EqualFold(value, "yes") {
				value = "true"
			} else if strings.EqualFold(value, "no") {
				value = "false"
			}
		}
		fields = append(fields, key+"=\""+strings.ReplaceAll(value, "\"", "\"\"")+"\"")
	}
	// 只有明确使用 ODBC 语法的连接串才需要转换。其他连接串保留原样，
	// 交给 SQL Server 驱动按自身的引用和转义规则解析。
	if !explicit && !hasDriver {
		return dsn, nil
	}
	// The native driver otherwise uses its own default TLS policy.
	if !hasEncrypt && (driverMajor >= 18 || driverMajor == 17 && usesAuthentication) {
		fields = append(fields, "encrypt=yes")
	}
	return strings.Join(fields, ";"), nil
}
