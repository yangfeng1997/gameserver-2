package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// ---- data model ----

type fieldInfo struct {
	GoName     string
	GoType     string
	YamlName   string
	ProtoType  string
	Message    bool
	Repeated   bool
	Required   bool
	Reload     bool
	Env        bool
	Secret     bool
	EnumValues []string
}

type messageInfo struct {
	Name    string
	Fields  []fieldInfo
	Root    bool
	Common  bool
	Server  string
	IsLog   bool // 标记是否为日志配置（含 LoggerGroupConfig）
}

// ---- entry point ----

func main() {
	var schemaDir, outDir string
	flag.StringVar(&schemaDir, "schema", "config/schema", "schema proto directory root")
	flag.StringVar(&outDir, "out", "config/gen", "generated config output directory")
	flag.Parse()

	serverDir := filepath.Join(schemaDir, "server")

	// 加载顶层共享类型（types.proto 等）
	sharedTypeMsgs, _ := parseProto(filepath.Join(schemaDir, "types.proto"))

	// 按 server 分组的 proto 路径映射
	type protoFile struct {
		server string
		path   string
	}
	protoFiles := make(map[string][]protoFile)
	var rootCount int
	_ = filepath.Walk(serverDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() || !strings.HasSuffix(info.Name(), ".proto") {
			return nil
		}
		if info.Name() == "options.proto" {
			return nil
		}
		rel, _ := filepath.Rel(serverDir, path)
		parts := strings.SplitN(filepath.ToSlash(rel), "/", 2)
		svr := parts[0]
		protoFiles[svr] = append(protoFiles[svr], protoFile{svr, path})
		return nil
	})

	for svr, files := range protoFiles {
		var svrMessages []messageInfo
		for _, f := range files {
			parsed, err := parseProto(f.path)
			if err != nil {
				fmt.Fprintf(os.Stderr, "parse %s: %v\n", f.path, err)
				os.Exit(1)
			}
			svrMessages = append(svrMessages, parsed...)
		}

		// 如果该服务引用了共享类型，将共享类型合并进来
		svrMessages = mergeSharedTypes(svrMessages, sharedTypeMsgs)

		// 过滤 root 消息
		var roots []messageInfo
		for _, msg := range svrMessages {
			if msg.Root {
				roots = append(roots, msg)
			}
		}
		if len(roots) == 0 {
			continue
		}

		// 校验同一 server 内不能有重名的 Reloader
		seenNames := make(map[string]string) // name → proto file
		for _, root := range roots {
			short := strings.TrimSuffix(root.Name, "Config")
			reloaderName := svr
			if strings.HasSuffix(short, "Log") {
				reloaderName += "_log"
			}
			if prev, exists := seenNames[reloaderName]; exists {
				fmt.Fprintf(os.Stderr, "configgen: duplicate Reloader name %q in server %q: %s and %s\n",
					reloaderName, svr, prev, root.Name)
				os.Exit(1)
			}
			seenNames[reloaderName] = root.Name
		}

		rootCount += len(roots)

		// 输出目录
		targetDir := filepath.Join(outDir, "server", svr)
		if err := os.MkdirAll(targetDir, 0o755); err != nil {
			fmt.Fprintf(os.Stderr, "mkdir %s: %v\n", targetDir, err)
			os.Exit(1)
		}

		for _, root := range roots {
			short := strings.TrimSuffix(root.Name, "Config")
			fileName := camelToSnake(short) + "_config.go"
			yamlName := camelToSnake(short) + ".yaml"
			outPath := filepath.Join(targetDir, fileName)
			content := renderConfigFile(root, svr, svrMessages)
			if err := os.WriteFile(outPath, []byte(content), 0o644); err != nil {
				fmt.Fprintf(os.Stderr, "write %s: %v\n", outPath, err)
				os.Exit(1)
			}
			fmt.Printf("  %s/%s → %s\n", svr, yamlName, outPath)
		}
	}
	fmt.Printf("configgen: generated %d root config files\n", rootCount)
}

// ---- proto parser ----

func parseProto(path string) ([]messageInfo, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	lines := strings.Split(string(data), "\n")
	var messages []messageInfo
	for i := 0; i < len(lines); i++ {
		line := stripComment(strings.TrimSpace(lines[i]))
		if !strings.HasPrefix(line, "message ") || !strings.Contains(line, "{") {
			continue
		}
		name := strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(line, "message "), "{"))
		msg := messageInfo{Name: name}
		for i++; i < len(lines); i++ {
			fieldLine := stripComment(strings.TrimSpace(lines[i]))
			if fieldLine == "}" {
				break
			}
			if fieldLine == "" {
				continue
			}
			if parseMessageOption(&msg, fieldLine) {
				continue
			}
			field, ok, err := parseField(fieldLine)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", path, err)
			}
			if ok {
				msg.Fields = append(msg.Fields, field)
			}
		}
		// 标记是否为日志配置
		for _, f := range msg.Fields {
			if f.GoType == "LoggerGroupConfig" {
				msg.IsLog = true
				break
			}
		}
		messages = append(messages, msg)
	}
	return messages, nil
}

func parseMessageOption(msg *messageInfo, line string) bool {
	if strings.Contains(line, "(config.root)") {
		msg.Root = strings.Contains(line, "true")
		return true
	}
	if strings.Contains(line, "(config.server)") {
		msg.Server = parseOptionString(line)
		return true
	}
	if strings.Contains(line, "(config.common)") {
		msg.Common = strings.Contains(line, "true")
		return true
	}
	return strings.HasPrefix(line, "option ")
}

func parseOptionString(line string) string {
	idx := strings.Index(line, "\"")
	if idx < 0 {
		return ""
	}
	line = line[idx+1:]
	end := strings.Index(line, "\"")
	if end < 0 {
		return ""
	}
	return line[:end]
}

func stripComment(line string) string {
	if idx := strings.Index(line, "//"); idx >= 0 {
		line = line[:idx]
	}
	return strings.TrimSpace(line)
}

func parseField(line string) (fieldInfo, bool, error) {
	line = strings.TrimSuffix(line, ";")
	if line == "" || strings.HasPrefix(line, "option ") || strings.HasPrefix(line, "import ") {
		return fieldInfo{}, false, nil
	}
	options := ""
	if idx := strings.Index(line, "["); idx >= 0 {
		options = line[idx:]
		line = strings.TrimSpace(line[:idx])
	}
	if idx := strings.Index(line, "="); idx >= 0 {
		line = strings.TrimSpace(line[:idx])
	}
	parts := strings.Fields(line)
	if len(parts) < 2 {
		return fieldInfo{}, false, nil
	}
	repeated := false
	if parts[0] == "repeated" {
		repeated = true
		parts = parts[1:]
	}
	protoType := parts[0]
	protoName := parts[1]
	field := fieldInfo{
		GoName:    snakeToPascal(protoName),
		YamlName:  protoName,
		ProtoType: protoType,
		Repeated:  repeated,
	}
	field.GoType, field.Message = goType(protoType, repeated)
	field.Required = strings.Contains(options, "config.required")
	field.Reload = strings.Contains(options, "config.reload")
	field.Env = strings.Contains(options, "config.env")
	field.Secret = strings.Contains(options, "config.secret")
	if field.Reload && field.Env {
		return fieldInfo{}, false, fmt.Errorf("field %s cannot set both reload and env", protoName)
	}
	if field.Reload && field.Secret {
		return fieldInfo{}, false, fmt.Errorf("field %s cannot set both reload and secret", protoName)
	}
	field.EnumValues = parseEnumValues(options)
	return field, true, nil
}

func goType(protoType string, repeated bool) (string, bool) {
	base := ""
	message := false
	switch protoType {
	case "string":
		base = "string"
	case "int32", "sint32", "sfixed32":
		base = "int32"
	case "int64", "sint64", "sfixed64":
		base = "int64"
	case "uint32", "fixed32":
		base = "uint32"
	case "uint64", "fixed64":
		base = "uint64"
	case "bool":
		base = "bool"
	case "float":
		base = "float32"
	case "double":
		base = "float64"
	default:
		base = protoType
		message = true
	}
	if repeated {
		return "[]" + base, message
	}
	return base, message
}

func parseEnumValues(options string) []string {
	needle := "config.enum_values) = \""
	idx := strings.Index(options, needle)
	if idx < 0 {
		return nil
	}
	start := idx + len(needle)
	end := strings.Index(options[start:], "\"")
	if end < 0 {
		return nil
	}
	parts := strings.Split(options[start:start+end], ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

// ---- code generator ----

func renderConfigFile(root messageInfo, svr string, allMsgs []messageInfo) string {
	var b strings.Builder
	configName := root.Name

	// 收集所有引用的子消息（非 root 消息）
	subMsgs := collectSubMessages(root, allMsgs)

	// ---- file header ----
	b.WriteString("// Code generated by tools/configgen. DO NOT EDIT.\n")
	b.WriteString("package " + svr + "\n\n")
	b.WriteString("import (\n")
	b.WriteString("\t\"bytes\"\n")
	b.WriteString("\t\"fmt\"\n")
	b.WriteString("\t\"os\"\n")
	b.WriteString("\t\"reflect\"\n")
	b.WriteString("\t\"sync/atomic\"\n\n")
	b.WriteString("\t\"gopkg.in/yaml.v3\"\n")
	b.WriteString("\t\"project/src/core/config\"\n")
	b.WriteString(")\n\n")

	shortName := strings.TrimSuffix(configName, "Config")
	reloaderName := unexported(shortName) + "ConfigReloader"
	reloaderVar := shortName + "ConfigReloader"
	dataVar := unexported(shortName) + "ConfigData"
	pathConst := shortName + "ConfigPath"
	loadFunc := "Load" + shortName + "Config"
	reloadFunc := "Reload" + shortName + "Config"
	checkFunc := "Check" + shortName + "ConfigReload"
	snapFunc := shortName + "ConfigSnapshot"
	restoreFunc := shortName + "ConfigRestore"
	instanceFunc := shortName + "ConfigInstance"
	yamlFile := camelToSnakeYAML(configName) + ".yaml"
	// Common config 放在 run/common/conf/，服务从 run/<svc>/bin/ 用 ../../common/conf/ 访问
	pathValue := "../conf/" + yamlFile
	if root.Common {
		pathValue = "../../common/conf/" + yamlFile
	}

	// ---- const ----
	b.WriteString("const " + pathConst + " = \"" + pathValue + "\"\n\n")

	// ---- root type ----
	b.WriteString("type " + configName + " struct {\n")
	for _, f := range root.Fields {
		b.WriteString("\t" + f.GoName + " " + f.GoType + " `yaml:\"" + f.YamlName + "\"`\n")
	}
	b.WriteString("}\n\n")

	// ---- sub-message types ----
	for _, sub := range subMsgs {
		b.WriteString("// " + sub.Name + " 配置子结构。\n")
		b.WriteString("type " + sub.Name + " struct {\n")
		for _, f := range sub.Fields {
			b.WriteString("\t" + f.GoName + " " + f.GoType + " `yaml:\"" + f.YamlName + "\"`\n")
		}
		b.WriteString("}\n\n")

		// Validate for sub-messages
		renderValidate(&b, sub)
		b.WriteString("\n")

		// Redacted for sub-messages that have (or contain) secret fields
		if hasSecretFieldRecursive(sub, allMsgs) {
			msgMap := make(map[string]messageInfo)
			for _, m := range allMsgs {
				msgMap[m.Name] = m
			}
			renderRedacted(&b, sub, sub.Name, msgMap)
		}
	}

	// ---- reloader type + var ----
	b.WriteString("// " + reloaderName + " 实现 config.Reloader 接口，供 Manager 注册。\n")
	b.WriteString("type " + reloaderName + " struct{\n")
	b.WriteString("\tsnapshot *" + configName + "\n")
	b.WriteString("}\n\n")
	b.WriteString("var " + dataVar + " atomic.Pointer[" + configName + "]\n")
	b.WriteString("var " + reloaderVar + " " + reloaderName + "\n\n")

	// ---- Validate ----
	renderValidate(&b, root)
	b.WriteString("\n")

	// ---- Load ----
	b.WriteString("// " + loadFunc + " 起服时加载配置（主 goroutine 调用，无锁）。\n")
	b.WriteString("func " + loadFunc + "() error {\n")
	b.WriteString("\tdata, err := os.ReadFile(" + pathConst + ")\n")
	b.WriteString("\tif err != nil {\n")
	b.WriteString("\t\treturn fmt.Errorf(\"load " + yamlFile + ": %w\", err)\n")
	b.WriteString("\t}\n")
	b.WriteString("\tdata, err = config.ExpandEnv(data)\n")
	b.WriteString("\tif err != nil {\n")
	b.WriteString("\t\treturn fmt.Errorf(\"load " + yamlFile + ": %w\", err)\n")
	b.WriteString("\t}\n")
	b.WriteString("\tvar cfg " + configName + "\n")
	b.WriteString("\tdec := yaml.NewDecoder(bytes.NewReader(data))\n")
	b.WriteString("\tdec.KnownFields(true)\n")
	b.WriteString("\tif err := dec.Decode(&cfg); err != nil {\n")
	b.WriteString("\t\treturn fmt.Errorf(\"load " + yamlFile + ": %w\", err)\n")
	b.WriteString("\t}\n")
	b.WriteString("\tif err := cfg.Validate(); err != nil {\n")
	b.WriteString("\t\treturn fmt.Errorf(\"validate " + yamlFile + ": %w\", err)\n")
	b.WriteString("\t}\n")
	b.WriteString("\t" + dataVar + ".Store(&cfg)\n")
	b.WriteString("\treturn nil\n")
	b.WriteString("}\n\n")

	// ---- Reload ----
	b.WriteString("// " + reloadFunc + " 热更配置（主 goroutine 调用，无锁）。\n")
	b.WriteString("func " + reloadFunc + "() error {\n")
	b.WriteString("\tdata, err := os.ReadFile(" + pathConst + ")\n")
	b.WriteString("\tif err != nil {\n")
	b.WriteString("\t\treturn fmt.Errorf(\"reload " + yamlFile + ": %w\", err)\n")
	b.WriteString("\t}\n")
	b.WriteString("\tdata, err = config.ExpandEnv(data)\n")
	b.WriteString("\tif err != nil {\n")
	b.WriteString("\t\treturn fmt.Errorf(\"reload " + yamlFile + ": %w\", err)\n")
	b.WriteString("\t}\n")
	b.WriteString("\tvar candidate " + configName + "\n")
	b.WriteString("\tdec := yaml.NewDecoder(bytes.NewReader(data))\n")
	b.WriteString("\tdec.KnownFields(true)\n")
	b.WriteString("\tif err := dec.Decode(&candidate); err != nil {\n")
	b.WriteString("\t\treturn fmt.Errorf(\"reload " + yamlFile + ": %w\", err)\n")
	b.WriteString("\t}\n")
	b.WriteString("\tif err := candidate.Validate(); err != nil {\n")
	b.WriteString("\t\treturn fmt.Errorf(\"validate " + yamlFile + ": %w\", err)\n")
	b.WriteString("\t}\n")
	b.WriteString("\tcurrent := " + dataVar + ".Load()\n")
	b.WriteString("\tif current != nil {\n")
	b.WriteString("\t\tif err := " + checkFunc + "(&candidate, current); err != nil {\n")
	b.WriteString("\t\t\treturn fmt.Errorf(\"check reload " + yamlFile + ": %w\", err)\n")
	b.WriteString("\t\t}\n")
	b.WriteString("\t}\n")
	b.WriteString("\t" + dataVar + ".Store(&candidate)\n")
	b.WriteString("\treturn nil\n")
	b.WriteString("}\n\n")

	// ---- CheckReload ----
	renderCheckReload(&b, root, checkFunc, configName, allMsgs)

	// ---- Snapshot + Restore ----
	b.WriteString("// " + snapFunc + " 返回当前配置快照（供 Manager 回滚）。\n")
	b.WriteString("func " + snapFunc + "() *" + configName + " {\n")
	b.WriteString("\treturn " + dataVar + ".Load()\n")
	b.WriteString("}\n\n")

	b.WriteString("// " + restoreFunc + " 回滚到快照值（供 Manager 回滚）。\n")
	b.WriteString("func " + restoreFunc + "(old *" + configName + ") {\n")
	b.WriteString("\tif old != nil {\n")
	b.WriteString("\t\t" + dataVar + ".Store(old)\n")
	b.WriteString("\t}\n")
	b.WriteString("}\n\n")

	// ---- Instance ----
	b.WriteString("// " + instanceFunc + " 返回当前配置（热路径，atomic.Load，无锁）。\n")
	b.WriteString("func " + instanceFunc + "() *" + configName + " {\n")
	b.WriteString("\treturn " + dataVar + ".Load()\n")
	b.WriteString("}\n\n")

	// ---- Redacted ----
	hasSecret := hasSecretFieldRecursive(root, allMsgs)
	if hasSecret {
		msgMap := make(map[string]messageInfo)
		for _, m := range allMsgs {
			msgMap[m.Name] = m
		}
		renderRedacted(&b, root, configName, msgMap)
	}

	// ---- Reloader methods ----
	b.WriteString("func (r *" + reloaderName + ") Name() string { return \"" + svr)
	if strings.HasSuffix(shortName, "Log") {
		b.WriteString("_log")
	}
	b.WriteString("\" }\n")
	b.WriteString("func (r *" + reloaderName + ") Reload() error { return " + reloadFunc + "() }\n")
	b.WriteString("func (r *" + reloaderName + ") SaveSnapshot()  { r.snapshot = " + snapFunc + "() }\n")
	b.WriteString("func (r *" + reloaderName + ") RestoreSnapshot() {\n")
	b.WriteString("\tif r.snapshot != nil {\n")
	b.WriteString("\t\t" + restoreFunc + "(r.snapshot)\n")
	b.WriteString("\t}\n")
	b.WriteString("}\n")

	return b.String()
}

func renderValidate(b *strings.Builder, msg messageInfo) {
	b.WriteString("// Validate 校验配置必填字段与枚举值。\n")
	b.WriteString("func (c *" + msg.Name + ") Validate() error {\n")
	b.WriteString("\tif c == nil {\n")
	b.WriteString("\t\treturn fmt.Errorf(\"" + unexported(msg.Name) + " is nil\")\n")
	b.WriteString("\t}\n")
	for _, f := range msg.Fields {
		writeFieldValidation(b, f)
	}
	b.WriteString("\treturn nil\n")
	b.WriteString("}\n")
}

func writeFieldValidation(b *strings.Builder, f fieldInfo) {
	if f.Message && !f.Repeated {
		b.WriteString("\tif err := c." + f.GoName + ".Validate(); err != nil {\n")
		b.WriteString("\t\treturn fmt.Errorf(\"" + f.YamlName + ": %w\", err)\n")
		b.WriteString("\t}\n")
		return
	}
	if f.Required && !f.Secret {
		expr := requiredExpr("c."+f.GoName, f.GoType, f.Repeated)
		if expr != "" {
			b.WriteString("\tif " + expr + " {\n")
			b.WriteString("\t\treturn fmt.Errorf(\"" + f.YamlName + " is required\")\n")
			b.WriteString("\t}\n")
		}
	}
	if len(f.EnumValues) > 0 && f.GoType == "string" {
		b.WriteString("\tif c." + f.GoName + " != \"\" {\n")
		b.WriteString("\t\tswitch c." + f.GoName + " {\n")
		b.WriteString("\t\tcase ")
		for i, v := range f.EnumValues {
			if i > 0 {
				b.WriteString(", ")
			}
			b.WriteString(fmt.Sprintf("%q", v))
		}
		b.WriteString(":\n")
		b.WriteString("\t\tdefault:\n")
		b.WriteString("\t\t\treturn fmt.Errorf(\"" + f.YamlName + "=%q is invalid\", c." + f.GoName + ")\n")
		b.WriteString("\t\t}\n")
		b.WriteString("\t}\n")
	}
}

func renderCheckReload(b *strings.Builder, msg messageInfo, funcName, configName string, allMsgs []messageInfo) {
	b.WriteString("// " + funcName + " 校验 candidate 相对 current 的变更是否合法。\n")
	b.WriteString("// 仅标记 (config.reload) 的字段允许变更，其余字段变更会被拒绝。\n")
	b.WriteString("func " + funcName + "(candidate, current *" + configName + ") error {\n")
	b.WriteString("\tif candidate == nil || current == nil {\n")
	b.WriteString("\t\treturn nil\n")
	b.WriteString("\t}\n")
	msgMap := make(map[string]messageInfo)
	for _, m := range allMsgs {
		msgMap[m.Name] = m
	}
	writeReloadChecks(b, msg, msgMap, "candidate", "current", "")
	b.WriteString("\treturn nil\n")
	b.WriteString("}\n")
}

func writeReloadChecks(b *strings.Builder, msg messageInfo, msgMap map[string]messageInfo, candidate, current, prefix string) {
	for _, f := range msg.Fields {
		path := f.YamlName
		if prefix != "" {
			path = prefix + "." + f.YamlName
		}
		candExpr := candidate + "." + f.GoName
		currExpr := current + "." + f.GoName
		if f.Message && !f.Repeated {
			child, ok := msgMap[f.GoType]
			if ok {
				writeReloadChecks(b, child, msgMap, candExpr, currExpr, path)
			}
			continue
		}
		if f.Reload {
			continue
		}
		b.WriteString("\tif !reflect.DeepEqual(" + candExpr + ", " + currExpr + ") {\n")
		b.WriteString("\t\treturn fmt.Errorf(\"" + path + " cannot reload\")\n")
		b.WriteString("\t}\n")
	}
}

func renderRedacted(b *strings.Builder, msg messageInfo, configName string, msgMap map[string]messageInfo) {
	b.WriteString("// Redacted 返回打码后的配置副本，标记了 (config.secret) 的字段替换为 \"***\"。\n")
	b.WriteString("// 供日志打印等场景使用，避免泄露敏感值。\n")
	b.WriteString("func (c *" + configName + ") Redacted() *" + configName + " {\n")
	b.WriteString("\tif c == nil {\n")
	b.WriteString("\t\treturn nil\n")
	b.WriteString("\t}\n")
	b.WriteString("\tclone := *c\n")
	writeRedactedFields(b, msg, "clone", msgMap)
	b.WriteString("\treturn &clone\n")
	b.WriteString("}\n\n")
}

func writeRedactedFields(b *strings.Builder, msg messageInfo, varName string, msgMap map[string]messageInfo) {
	for _, f := range msg.Fields {
		fieldExpr := varName + "." + f.GoName
		if f.Secret {
			if f.Repeated {
				b.WriteString("\t" + fieldExpr + " = nil\n")
			} else {
				switch f.GoType {
				case "string":
					b.WriteString("\t" + fieldExpr + " = \"***\"\n")
				case "int32", "int64", "uint32", "uint64", "float32", "float64":
					b.WriteString("\t" + fieldExpr + " = 0\n")
				default:
					b.WriteString("\t" + fieldExpr + " = \"***\"\n")
				}
			}
		} else if f.Message && !f.Repeated {
			// Only recurse if the nested message type actually has secrets
			typeName := f.GoType
			if sub, ok := msgMap[typeName]; ok && secretInMap(sub, msgMap) {
				b.WriteString("\t" + fieldExpr + " = *" + fieldExpr + ".Redacted()\n")
			}
		}
	}
}

func hasSecretField(msg messageInfo) bool {
	for _, f := range msg.Fields {
		if f.Secret {
			return true
		}
	}
	return false
}

// hasSecretFieldRecursive checks if the root message or any referenced sub-message has secret fields.
func hasSecretFieldRecursive(msg messageInfo, allMsgs []messageInfo) bool {
	msgMap := make(map[string]messageInfo)
	for _, m := range allMsgs {
		msgMap[m.Name] = m
	}
	return secretInMap(msg, msgMap)
}

// secretInMap checks if msg or any of its field-type descendants (looked up in msgMap) has secret fields.
func secretInMap(msg messageInfo, msgMap map[string]messageInfo) bool {
	visited := make(map[string]bool)
	var check func(messageInfo) bool
	check = func(m messageInfo) bool {
		if visited[m.Name] {
			return false
		}
		visited[m.Name] = true
		for _, f := range m.Fields {
			if f.Secret {
				return true
			}
			if f.Message {
				typeName := f.GoType
				if f.Repeated {
					typeName = strings.TrimPrefix(typeName, "[]")
				}
				if sub, ok := msgMap[typeName]; ok {
					if check(sub) {
						return true
					}
				}
			}
		}
		return false
	}
	return check(msg)
}

// mergeSharedTypes merges shared type messages (from types.proto) that are referenced
// by any message in svrMessages. This ensures generated files include needed sub-types.
func mergeSharedTypes(svrMessages, sharedTypeMsgs []messageInfo) []messageInfo {
	msgMap := make(map[string]messageInfo)
	for _, m := range svrMessages {
		msgMap[m.Name] = m
	}
	sharedMap := make(map[string]messageInfo)
	for _, m := range sharedTypeMsgs {
		sharedMap[m.Name] = m
	}

	// Check which shared types are referenced
	var extra []messageInfo
	for _, msg := range svrMessages {
		for _, f := range msg.Fields {
			if !f.Message {
				continue
			}
			typeName := f.GoType
			if f.Repeated {
				typeName = strings.TrimPrefix(typeName, "[]")
			}
			if _, exists := msgMap[typeName]; exists {
				continue
			}
			if shared, ok := sharedMap[typeName]; ok {
				msgMap[typeName] = shared
				extra = append(extra, shared)
				// recursively add sub-types of the shared type
				extra = append(extra, collectSubMessagesForType(shared, sharedMap)...)
			}
		}
	}
	return append(svrMessages, extra...)
}

func collectSubMessagesForType(msg messageInfo, msgMap map[string]messageInfo) []messageInfo {
	visited := map[string]bool{msg.Name: true}
	var result []messageInfo
	var collect func(messageInfo)
	collect = func(m messageInfo) {
		for _, f := range m.Fields {
			if !f.Message {
				continue
			}
			typeName := f.GoType
			if f.Repeated {
				typeName = strings.TrimPrefix(typeName, "[]")
			}
			sub, ok := msgMap[typeName]
			if !ok || visited[typeName] {
				continue
			}
			visited[typeName] = true
			result = append(result, sub)
			collect(sub)
		}
	}
	collect(msg)
	return result
}

// collectSubMessages returns non-root messages that are referenced as field types by the root message.
// E.g., CommonConfig references ClusterConfig, EtcdConfig, etc. — these need structs generated too.
func collectSubMessages(root messageInfo, allMsgs []messageInfo) []messageInfo {
	msgMap := make(map[string]messageInfo)
	for _, m := range allMsgs {
		msgMap[m.Name] = m
	}

	visited := make(map[string]bool)
	var result []messageInfo
	var collect func(msg messageInfo)
	collect = func(msg messageInfo) {
		for _, f := range msg.Fields {
			if !f.Message {
				continue
			}
			// For repeated message fields, the GoType is like "[]RedisNodeConfig"
			typeName := f.GoType
			if f.Repeated {
				typeName = strings.TrimPrefix(typeName, "[]")
			}
			sub, ok := msgMap[typeName]
			if !ok || visited[sub.Name] {
				continue
			}
			visited[sub.Name] = true
			result = append(result, sub)
			collect(sub) // recurse into nested sub-messages
		}
	}
	collect(root)
	return result
}

func requiredExpr(name, goType string, repeated bool) string {
	if repeated {
		return "len(" + name + ") == 0"
	}
	switch goType {
	case "string":
		return name + " == \"\""
	case "int32", "int64", "uint32", "uint64", "float32", "float64":
		return name + " == 0"
	}
	return ""
}

// ---- naming utilities ----

func snakeToPascal(s string) string {
	parts := strings.Split(s, "_")
	for i, part := range parts {
		if part == "" {
			continue
		}
		parts[i] = strings.ToUpper(part[:1]) + part[1:]
	}
	return strings.Join(parts, "")
}

func snakeToLower(s string) string {
	return strings.ToLower(s)
}

// camelToSnake converts CamelCase to snake_case: "GatesvrLog" → "gatesvr_log".
func camelToSnake(s string) string {
	var b strings.Builder
	for i, r := range s {
		if r >= 'A' && r <= 'Z' {
			if i > 0 {
				b.WriteByte('_')
			}
			b.WriteRune(r + 32) // to lower
		} else {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// camelToSnakeYAML converts CamelCase to snake_case for YAML filename:
// "GatesvrLog" → "gatesvr_log"
func camelToSnakeYAML(configName string) string {
	short := strings.TrimSuffix(configName, "Config")
	return camelToSnake(short)
}

func unexported(s string) string {
	if s == "" {
		return ""
	}
	return strings.ToLower(s[:1]) + s[1:]
}

// scanServers returns unique server directories under config/schema/server/.
func scanServers(schemaDir string) ([]string, error) {
	serverDir := filepath.Join(schemaDir, "server")
	entries, err := os.ReadDir(serverDir)
	if err != nil {
		return nil, err
	}
	var servers []string
	for _, e := range entries {
		if e.IsDir() {
			servers = append(servers, e.Name())
		}
	}
	sort.Strings(servers)
	return servers, nil
}
