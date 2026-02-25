# spys.one SOCKS 抓取器（FlareSolverr Only）

当前版本已精简为 **仅通过 FlareSolverr 过盾并抓取**，不再使用浏览器兜底或直连抓取。

支持配置文件启动。

优先级：`命令行参数 > 配置文件 > 默认值`

默认会自动查找 `socks-grabber.json`：
- 可执行文件同目录（如 `socks-grabber` 或 `socks-grabber.exe`）
- 当前工作目录

也可手动指定：`-config "/path/to/socks-grabber.json"`

程序会自动检测：
- 可执行文件同目录下是否存在 `flaresolverr/flaresolverr`（Windows 为 `flaresolverr.exe`）
- 如果 FlareSolverr 未运行且该文件存在，会自动启动并等待服务就绪后再抓取

如果同级目录没有 FlareSolverr，程序会按当前操作系统自动下载最新版并解压到同级目录：
- Windows: `flaresolverr_windows_x64.zip`
- Linux: `flaresolverr_linux_x64.tar.gz`

下载后目录结构为：`./flaresolverr/...`

## 先启动 FlareSolverr

默认接口地址：`http://127.0.0.1:8191/v1`

## CLI 运行

```bash
go run . -out alive.txt -csv-out all_proxies.csv -flaresolverr-url "http://127.0.0.1:8191/v1" -retries 3
```

## 源代码运行模式

直接运行源码（跨平台）：

```bash
go run .
```

源码命令行模式（不进 GUI）：

```bash
go run . -gui=false
```

源码指定配置文件：

```bash
go run . -config "/path/to/socks-grabber.json"
```

先编译再运行：

```bash
go build -o socks-grabber .
./socks-grabber -gui=false
```

Windows 编译运行示例：

```bash
go build -o socks-grabber.exe .
socks-grabber.exe -gui=false
```

## 常用命令

直接运行（读取同目录 `socks-grabber.json`）：

```bash
./socks-grabber
```

命令行模式（不进 GUI）：

```bash
./socks-grabber -gui=false
```

指定配置文件：

```bash
./socks-grabber -config "/path/to/socks-grabber.json"
```

导出可用代理到 txt + 全量结果到 csv：

```bash
./socks-grabber -gui=false -out alive.txt -csv-out all_proxies.csv
```

抓取最大页（最多 500）：

```bash
./socks-grabber -gui=false -page-size 500
```

`-page-size` 可选值：`30/50/100/200/300/500`。如果输入其他值，会自动归一化到最接近的可选值。

关闭检测（只抓取，不测可用性）：

```bash
./socks-grabber -gui=false -check=false
```

自定义测试网址（默认 AWS）：

```bash
./socks-grabber -gui=false -check-url "https://aws.amazon.com"
```

Windows 可直接用：`socks-grabber.exe ...`

说明：
- `txt`（`-out`）只导出可用代理，格式：`协议://ip:port`
- `csv`（`-csv-out`）导出全部代理及检测结果（可用/不可用都会保留）

CSV 列：`protocol,ip,port,proxy,alive,latency_ms,error,source`

抓取条数默认会请求最大页：

```bash
go run . -gui=false -page-size 500
```

配置文件示例（`socks-grabber.json`）：

```json
{
  "url": "https://spys.one/en/socks-proxy-list/",
  "out": "alive.txt",
  "csv_out": "all_proxies.csv",
  "json": false,
  "timeout": "3m",
  "flaresolverr_url": "http://127.0.0.1:8191/v1",
  "flaresolverr_timeout": "90s",
  "page_size": 500,
  "retries": 3,
  "retry_backoff": "3s",
  "retry_jitter": "1200ms",
  "check": true,
  "only_alive": true,
  "check_workers": 64,
  "check_timeout": "3s",
  "check_url": "https://aws.amazon.com",
  "socks5_handshake": true,
  "gui": false,
  "gui_addr": "127.0.0.1:8090"
}
```

只抓取不做可用性检测：

```bash
go run . -out alive.txt -csv-out all_proxies.csv -check=false
```

说明：关闭检测后，CSV 中 `error` 会标记为 `not_checked`。

JSON 输出：

```bash
go run . -out proxies.json -json
```

## GUI 模式

```bash
go run . -gui=true
```

如果配置文件里 `"gui": true`，直接运行也会进入 GUI：

```bash
./socks-grabber
```

默认地址：`http://127.0.0.1:8090`

自定义地址：

```bash
go run . -gui=true -gui-addr "127.0.0.1:9000"
```

## 参数

- `-url` 目标地址（默认 `https://spys.one/en/socks-proxy-list/`）
- `-config` 指定 JSON 配置文件路径
- `-out` 输出文件
- `-csv-out` CSV 输出文件（全部代理+检测结果）
- `-json` JSON 输出
- `-timeout` 全局超时
- `-flaresolverr-url` FlareSolverr v1 地址
- `-flaresolverr-timeout` FlareSolverr `maxTimeout`
- `-page-size` 单页目标条数（30/50/100/200/300/500）
- `-retries` 重试次数
- `-retry-backoff` 退避基础时间
- `-retry-jitter` 退避抖动
- `-check` 是否检测可用性
- `-only-alive` 仅输出可用代理
- `-check-workers` 检测并发
- `-check-timeout` 单代理检测超时
- `-check-url` 通过代理访问测试网址（默认 `https://aws.amazon.com`）
- `-socks5-handshake` 是否做 SOCKS5 握手
- `-gui` 启动 GUI
- `-gui-addr` GUI 监听地址
