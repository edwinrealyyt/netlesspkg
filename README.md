# NetlessPkg

**离线环境通用包管理工具** — 让无网络的 Linux 服务器也能轻松安装软件。

NetlessPkg 通过"内网导出 → 外网同步 → 内网安装"的跨网工作流，解决隔离网络环境下的软件包安装难题。支持 **APT (Debian/Ubuntu)** 和 **YUM/DNF (RHEL/CentOS/Rocky)** 两大主流包管理体系，自动递归解析全量依赖并打包为单个 Bundle 文件，实现真正的一键离线安装。

## ✨ 特性

- 🔌 **全离线工作流** — 内网机器无需任何网络连接，所有下载均在外网完成
- 📦 **完整依赖解析** — 递归计算依赖闭包，自动排除系统已安装的包，避免版本冲突
- 🔄 **内网源自动映射** — 内置阿里云/腾讯云/华为云内网源到公网镜像的自动转换
- 📁 **单文件 Bundle** — 元数据和安装包各打包为一个 `.bundle` 文件，传输简单
- ✅ **SHA256 完整性校验** — 支持下载包的哈希校验，确保传输安全
- ⚡ **并发下载 + 断点续传** — 高效下载大量依赖包
- 🏗️ **跨平台构建** — 单个 Go 二进制，支持 Linux (amd64/arm64/arm) 和 Windows

## 📋 工作流概览

```
 ┌──────────────────────────────────────────────────────────────────┐
 │                        典型跨网工作流                             │
 │                                                                  │
 │  ┌─────────┐   meta_request   ┌─────────┐   metadata   ┌──────┐ │
 │  │  内网机  │ ──────.json────→ │  外网机  │ ───.bundle──→│内网机│ │
 │  │ export  │                  │sync-meta│              │ plan │ │
 │  └─────────┘                  └─────────┘              └──┬───┘ │
 │                                                           │     │
 │  ┌─────────┐  packages.bundle ┌─────────┐  download_plan │     │
 │  │  内网机  │ ←────────────── │  外网机  │ ←───.json─────┘     │
 │  │ install │                  │  fetch  │                      │
 │  └─────────┘                  └─────────┘                      │
 └──────────────────────────────────────────────────────────────────┘
```

## 🚀 快速开始

### 安装

从 [Releases](https://github.com/edwinrealyyt/netlesspkg/releases) 下载对应平台的二进制文件，或从源码构建：

```bash
# 克隆仓库
git clone https://github.com/edwinrealyyt/netlesspkg.git
cd netlesspkg

# 构建（Linux/macOS）
bash build.sh

# 构建（Windows PowerShell）
.\build.ps1
```

构建产物位于 `dist/` 目录下。

### 使用示例：在离线内网服务器安装 Nginx

**第一步：内网机 — 导出源配置**

```bash
# 在内网目标机器上运行，扫描系统源配置，生成元数据下载清单
netlesspkg export -o meta_request.json
```

**第二步：外网机 — 同步元数据**

将 `meta_request.json` 拷贝到有网络的机器上：

```bash
# 下载系统源的索引数据库（Packages.gz / primary.xml.gz 等）
netlesspkg sync-meta -i meta_request.json -o metadata.bundle
```

**第三步：内网机 — 计算依赖**

将 `metadata.bundle` 拷回内网机器：

```bash
# 注入元数据并计算完整依赖树，生成下载计划
netlesspkg plan -i metadata.bundle -p nginx -o download_plan.json
```

**第四步：外网机 — 下载安装包**

将 `download_plan.json` 拷贝到外网机器：

```bash
# 按照计划下载所有依赖包，打包为单个 bundle
netlesspkg fetch -i download_plan.json -o packages.bundle
```

**第五步：内网机 — 离线安装**

将 `packages.bundle` 拷回内网，执行安装：

```bash
# 解压并离线安装
sudo netlesspkg install -i packages.bundle -p nginx
```

## 📖 命令参考

| 命令 | 说明 | 运行环境 |
|------|------|---------|
| `export` | 扫描系统源配置，导出元数据下载清单 | 内网 |
| `sync-meta` | 根据清单下载元数据，生成 `metadata.bundle` | 外网 |
| `plan` | 注入元数据并计算依赖，生成下载计划 | 内网 |
| `fetch` | 根据下载计划获取安装包，生成 `packages.bundle` | 外网 |
| `install` | 解压安装包并执行离线安装 | 内网 |
| `verify` | 校验 bundle 中文件的 SHA256 完整性 | 任意 |

### 高级参数

#### URL 重写（sync-meta / fetch 可用）

```bash
# 手动指定 URL 替换规则（可多次指定）
netlesspkg sync-meta -i meta_request.json \
  --replace mirrors.cloud.aliyuncs.com=mirrors.aliyun.com \
  --replace mirrors.tencentyun.com=mirrors.cloud.tencent.com

# 禁用内置的云厂商内网源自动映射
netlesspkg fetch -i download_plan.json --no-auto-replace
```

#### 并发下载

```bash
# 设置并发下载线程数（默认 4）
netlesspkg fetch -i download_plan.json -j 8
```

## 🏛️ 项目结构

```
netlesspkg/
├── main.go              # 入口
├── cmd/                 # 子命令实现
│   ├── root.go          # 命令路由 & 帮助信息
│   ├── export.go        # export 子命令
│   ├── syncmeta.go      # sync-meta 子命令
│   ├── plan.go          # plan 子命令
│   ├── fetch.go         # fetch 子命令
│   ├── install.go       # install 子命令
│   └── verify.go        # verify 子命令
├── pkg/
│   ├── core/            # 核心类型定义 & 包管理器注册表
│   ├── archive/         # Bundle 打包/解压 (tar.gz)
│   ├── downloader/      # 并发下载引擎 (断点续传/进度条/重试)
│   └── pm/              # 包管理器适配层
│       ├── apt/         # APT (Debian/Ubuntu) 实现
│       └── yum/         # YUM/DNF (RHEL/CentOS) 实现
├── build.sh             # Linux/macOS 构建脚本
├── build.ps1            # Windows 构建脚本
└── .gitignore
```

## 🔧 支持的系统

| 包管理器 | 发行版 | 状态 |
|---------|--------|------|
| APT | Debian, Ubuntu, 及其衍生版 | ✅ 支持 |
| YUM/DNF | RHEL, CentOS, Rocky Linux, AlmaLinux | ✅ 支持 |

## 📝 内置云厂商内网源映射

NetlessPkg 内置了常见云厂商内网源到公网镜像的自动映射，开箱即用：

| 云厂商 | 内网域名 | 公网域名 |
|--------|---------|---------|
| 阿里云 | `mirrors.cloud.aliyuncs.com` | `mirrors.aliyun.com` |
| 腾讯云 | `mirrors.tencentyun.com` | `mirrors.cloud.tencent.com` |
| 华为云 | `repo.huaweicloud.com` (内网) | `repo.huaweicloud.com` (公网) |

使用 `--no-auto-replace` 可禁用此行为；使用 `--replace` / `-r` 可追加自定义规则。

## 📄 License

MIT
