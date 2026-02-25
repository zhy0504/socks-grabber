# socks-grabber

一个基于 Go 的 SOCKS 代理抓取与检测工具。

## FlareSolverr 作用

`spys.one` 有 Cloudflare/反爬挑战，普通 HTTP 请求经常拿不到可解析页面。

FlareSolverr 在本项目中的作用是：
- 处理挑战页并返回可解析 HTML
- 让程序稳定获取代理列表
- 作为抓取链路的核心依赖

主要功能：
- 通过 FlareSolverr 抓取 `spys.one` 代理列表
- 自动处理 FlareSolverr：检测、启动、缺失时按系统自动下载
- 可用性检测（TCP、SOCKS5 握手、通过代理访问测试网址）
- 导出两份结果：
  - `txt`：仅可用代理，格式 `协议://ip:port`
  - `csv`：全部代理及检测结果（含失败原因）
- 支持 CLI 与 GUI

支持平台（与 FlareSolverr 自动下载能力保持一致）：
- Windows x64
- Linux x64

## 如何使用

默认优先级：`命令行参数 > 配置文件 > 默认值`

默认会自动查找配置文件 `socks-grabber.json`：
- 可执行文件同目录
- 当前工作目录

### 软件默认启动参数（无配置文件时）

- `url`: `https://spys.one/en/socks-proxy-list/`
- `out`: `proxies.txt`
- `csv-out`: `proxies.csv`
- `json`: `false`
- `timeout`: `3m`
- `flaresolverr-url`: `http://127.0.0.1:8191/v1`
- `flaresolverr-timeout`: `90s`
- `page-size`: `500`
- `retries`: `3`
- `retry-backoff`: `3s`
- `retry-jitter`: `1200ms`
- `check`: `true`
- `only-alive`: `true`
- `check-workers`: `64`
- `check-timeout`: `3s`
- `check-url`: `https://aws.amazon.com`
- `socks5-handshake`: `true`
- `gui`: `false`
- `gui-addr`: `127.0.0.1:8090`

说明：仓库里的 `socks-grabber.json` 是示例配置，当前设置为 `"gui": true`，因此直接运行会进入 GUI。

---

## 源代码启动

### 常用命令

直接运行（按配置执行）：

```bash
go run .
```

命令行模式（不进入 GUI）：

```bash
go run . -gui=false
```

抓取最大页（500）并导出 txt+csv：

```bash
go run . -gui=false -page-size 500 -out alive.txt -csv-out all_proxies.csv
```

关闭可用性检测（只抓取）：

```bash
go run . -gui=false -check=false
```

---

## 编译运行

### 常用命令

编译：

```bash
go build -o socks-grabber .
```

运行（按配置执行）：

```bash
./socks-grabber
```

命令行模式（不进入 GUI）：

```bash
./socks-grabber -gui=false
```

Windows 示例：

```bash
go build -o socks-grabber.exe .
socks-grabber.exe -gui=false
```

---

## 配置文件示例

文件名：`socks-grabber.json`

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
  "gui": true,
  "gui_addr": "127.0.0.1:8090"
}
```

说明：
- `page_size` 仅支持：`30/50/100/200/300/500`（其他值会归一化到最近档位）
- `out` 只写入可用代理
- `csv_out` 写入全部代理和测试结果

---

## 参数说明

- `-url` 目标地址（默认 `https://spys.one/en/socks-proxy-list/`）
- `-config` 指定 JSON 配置文件路径
- `-out` TXT 输出文件（仅可用代理）
- `-csv-out` CSV 输出文件（全部代理 + 结果）
- `-json` TXT 输出改为 JSON
- `-timeout` 全局超时
- `-flaresolverr-url` FlareSolverr 接口地址（默认 `http://127.0.0.1:8191/v1`）
- `-flaresolverr-timeout` FlareSolverr `maxTimeout`
- `-page-size` 单页条数（`30/50/100/200/300/500`）
- `-retries` FlareSolverr 抓取重试次数
- `-retry-backoff` 重试退避基础时长
- `-retry-jitter` 重试抖动时长
- `-check` 是否进行可用性检测
- `-only-alive` 是否只保留可用代理（影响最终 txt/json 输出）
- `-check-workers` 检测并发
- `-check-timeout` 单代理检测超时
- `-check-url` 通过代理访问的测试网址（默认 `https://aws.amazon.com`）
- `-socks5-handshake` 是否启用 SOCKS5 握手检测
- `-gui` 启动 GUI
- `-gui-addr` GUI 监听地址（默认 `127.0.0.1:8090`）

---

## FlareSolverr 自动处理

程序会自动处理 FlareSolverr，无需手工预启动：
- 先检测 `-flaresolverr-url` 是否可达
- 不可达则尝试启动同级目录 `./flaresolverr/` 下可执行文件
- 若同级目录不存在，会自动下载对应系统版本并解压后再启动

当前自动下载资产：
- Windows: `flaresolverr_windows_x64.zip`
- Linux: `flaresolverr_linux_x64.tar.gz`

GitHub Actions 也只编译以上平台版本（Windows x64 / Linux x64）。
