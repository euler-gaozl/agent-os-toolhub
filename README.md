# Agent OS ToolHub

独立的 **Skill / MCP 注册、检查、验活与启停服务**。拥有 `toolhub` schema 中的不可变资源版本、文件、激活状态、健康状态和审计记录；Registry 绑定它提供的确定版本。Agent 定义、模型密钥、执行回执与知识库分别由其他服务管理。

## 核心行为

- Skill ZIP 校验有大小与展开限制，拒绝目录穿越、重复/不安全路径、符号链接、可执行标记和疑似凭证。包内脚本不会在本机执行；静态检查不等于指令安全证明。
- MCP 使用固定的 Streamable HTTP 协议执行初始化和 `tools/list`。验活不会调用业务工具。公网要求 HTTPS，私网要求运维配置精确 `host:port` 白名单。
- 登记、发现、检查、执行共用 DNS/IP/重定向策略；新资源默认禁用，检测包含数据库租约、fence、有限并发、超时、重试和新鲜度检查。
- 工具定义发生漂移时阻止解析，不静默覆盖版本或激活状态。启用、禁用和检查保留审计。
- 真正调用跨 HTTP 进入 Runtime 的执行效果/回执事务；丢回复不能作为重复外部写入的依据。凭证从 Registry 按名称解析，不能直接读取凭证库。

## 独立部署

需要 Go 1.27.2、PostgreSQL 18；可用 PowerShell 7 执行构建。

```powershell
$env:GOWORK='off'; $env:GOPROXY='off'
./scripts/Build.ps1
go test -mod=vendor -count=1 ./...
go vet -mod=vendor ./...
```

Linux 可运行 `go build -mod=vendor ./cmd/toolhub`。提交的 `vendor` 与 [源码版本记录](source-provenance.json) 固定 SDK，不依赖相邻仓库、Node 或模型密钥。

根据 [配置示例](deploy/service.example.json) 创建受保护配置，用 `bin/toolhub --config <文件>` 启动。提供限定到 `search_path=toolhub` 的应用数据库账号、独立服务令牌/对端哈希（含自身）、Registry 与 Runtime 地址。`TOOLHUB_PROBE_WORKERS` 为 1..8，默认 2；HTTP 并发默认 4，连接池默认 3。容器或多机部署必须显式配置地址、私网/TLS 及资源限制。

## 迁移、升级与回滚

先创建非 schema 所有者的应用账号。DDL 连接仅通过 `MIGRATION_DATABASE_URL` 提供给 `bin/migrate --role <账号>`；迁移在有锁/语句期限的事务里更新本域及授权，包含审计序列。服务启动不执行 DDL、不复制其他服务的数据。

按 [发布契约](deploy/release-contract.json) 独立替换进程，保留数据库、配置、不可变版本和审计。只支持契约兼容的程序回滚，不执行破坏性的反向迁移。Windows 版本选择与正式激活由集成仓库管理。

## 验证范围

`TEST_DATABASE_URL` 启用独立 PostgreSQL 夹具，测试登记/启停、验活、定义漂移、租约恢复、权限和迁移回滚；不修改该 URL 指定的原数据库。未配置时明确跳过。Linux race、镜像及跨服务故障验证分别记录；模拟 MCP 通过不代表所有外部 MCP 均兼容或安全。

Actions 日常关闭，统一 CI 等待最终验收。[历史实现说明](docs/history/service-notes-20261014.en.md) 与 [平台计划](https://github.com/euler-gaozl/agent-os/blob/main/docs/windows-single-machine-plan.md) 保留具体进展。
