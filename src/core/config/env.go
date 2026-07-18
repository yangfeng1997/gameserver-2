package config

import (
	"fmt"
	"os"
	"regexp"
)

var envPattern = regexp.MustCompile(`\$\{[^}]+\}`)

// ExpandEnv 将 data 中的 ${NAME} 展开为对应环境变量的值。
// 若引用的环境变量未设置则报错。
func ExpandEnv(data []byte) ([]byte, error) {
	text := string(data)
	missing := make([]string, 0)
	text = envPattern.ReplaceAllStringFunc(text, func(token string) string {
		name := token[2 : len(token)-1] // "${NAME}" → "NAME"
		val, ok := os.LookupEnv(name)
		if !ok {
			missing = append(missing, name)
			return token
		}
		return val
	})
	if len(missing) > 0 {
		return nil, fmt.Errorf("env vars not injected: %v", missing)
	}
	return []byte(text), nil
}
