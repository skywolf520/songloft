# 插件源制作指南

本文档介绍如何创建和发布 Songloft 插件源（Plugin Registry），让其他用户通过订阅你的源地址，在应用内的「插件商店」浏览并安装你的插件。

---

## 什么是插件源

插件源是一个 JSON 文件，包含一组 `plugin.json` 的 URL 列表。用户在「设置 → JS 插件管理 → 插件商店 → 管理订阅源」中添加你的 JSON URL 后，即可看到源中的所有插件并一键安装。

后端会自动从每个 `plugin.json` URL 拉取插件的名称、版本、描述等信息，无需在源文件中重复填写。

插件源支持**嵌套引用**（`includes`），可以组合多个独立源，实现去中心化的插件分发。

---

## JSON 格式规范

```json
{
  "name": "我的插件源",
  "includes": [
    "https://example.com/other-registry.json"
  ],
  "plugins": [
    "https://raw.githubusercontent.com/you/example-plugin/main/plugin.json",
    "https://raw.githubusercontent.com/you/another-plugin/main/plugin.json"
  ]
}
```

### 字段说明

| 字段 | 类型 | 必填 | 说明 |
|------|------|------|------|
| `name` | string | 否 | 源名称，用于在 UI 中显示 |
| `includes` | string[] | 否 | 嵌套引用的其他源 URL 数组 |
| `plugins` | string[] | 是 | 各插件 `plugin.json` 的 URL 数组 |

### 自动解析机制

每个 `plugins` 中的 URL 指向插件仓库中的 `plugin.json`，后端会自动拉取并解析以下字段：

- `name` — 插件名称
- `entryPath` — 插件唯一标识符
- `version` — 版本号
- `description` — 描述
- `author` — 作者
- `homepage` — 项目主页
- `download_url` — ZIP 包下载地址
- `updateUrl` — 更新检查 URL
- `minHostVersion` — 最低宿主版本

如果 `plugin.json` 中没有 `download_url`（大多数插件是这种情况），后端会自动从 `updateUrl` 指向的 `manifest.json` 中获取。

---

## 完整示例

### 最小示例

```json
{
  "plugins": [
    "https://raw.githubusercontent.com/you/my-plugin/main/plugin.json"
  ]
}
```

### 含嵌套的完整示例

```json
{
  "name": "Songloft 社区聚合源",
  "includes": [
    "https://raw.githubusercontent.com/alice/songloft-plugins/main/registry.json",
    "https://raw.githubusercontent.com/bob/my-plugin-registry/main/registry.json"
  ],
  "plugins": [
    "https://raw.githubusercontent.com/songloft-org/songloft-plugin-miot/main/plugin.json",
    "https://raw.githubusercontent.com/songloft-org/songloft-plugin-subsonic/main/plugin.json"
  ]
}
```

---

## 发布到 GitHub

### 方式一：仓库内 Raw URL（推荐）

1. 在你的 GitHub 仓库根目录创建 `registry.json`
2. 推送到 `main` 分支
3. 源地址为：
   ```
   https://raw.githubusercontent.com/{用户名}/{仓库名}/main/registry.json
   ```

**插件 ZIP 托管**：推荐使用 GitHub Releases：
1. 在插件仓库创建 Release
2. 上传 `.jsplugin.zip` 作为 Release Asset
3. 在 `plugin.json` 中通过 `updateUrl` 指向 `manifest.json`，`manifest.json` 中填写 `download_url`：
   ```
   https://github.com/{用户名}/{仓库名}/releases/download/v{版本号}/{entry_path}.jsplugin.zip
   ```

### 方式二：GitHub Pages

1. 启用仓库的 GitHub Pages
2. 将 `registry.json` 放在 Pages 根目录
3. 源地址为：
   ```
   https://{用户名}.github.io/{仓库名}/registry.json
   ```

### 示例仓库结构

```
my-plugin-registry/
├── registry.json          ← 插件源 JSON（只需列出 plugin.json URL）
└── README.md
```

插件本身在各自的仓库中维护，例如：

```
my-songloft-plugin/
├── plugin.json            ← 插件元数据（name, version, entryPath 等）
├── manifest.json          ← 更新清单（version + download_url）
├── main.js                ← 插件入口
└── ...
```

---

## 发布到 JS CDN

### jsDelivr（通过 npm）

1. 创建 npm 包，包含 `registry.json`
2. 发布到 npm：
   ```bash
   npm publish
   ```
3. 源地址为：
   ```
   https://cdn.jsdelivr.net/npm/{包名}@latest/registry.json
   ```

### unpkg（通过 npm）

与 jsDelivr 类似，只需替换域名：
```
https://unpkg.com/{包名}@latest/registry.json
```

### jsDelivr（通过 GitHub）

不发布 npm 包也可以直接用 jsDelivr 加速 GitHub 文件：
```
https://cdn.jsdelivr.net/gh/{用户名}/{仓库名}@{分支}/registry.json
```

---

## 嵌套源的使用场景

`includes` 字段允许一个源引用其他源，递归下载并合并所有插件。

### 典型用法

**聚合源**：收集多个独立作者的源，方便用户一次性订阅：
```json
{
  "name": "社区聚合",
  "includes": [
    "https://raw.githubusercontent.com/alice/plugins/main/registry.json",
    "https://raw.githubusercontent.com/bob/plugins/main/registry.json",
    "https://raw.githubusercontent.com/charlie/plugins/main/registry.json"
  ],
  "plugins": []
}
```

**官方 + 社区**：官方源包含核心插件，同时引入社区贡献：
```json
{
  "name": "官方源",
  "includes": [
    "https://community.example.com/registry.json"
  ],
  "plugins": [
    "https://raw.githubusercontent.com/songloft-org/songloft-plugin-miot/main/plugin.json"
  ]
}
```

### 去重规则

当同一个 `entryPath` 出现在多个源（含嵌套）中时，保留**版本号更高**的条目。版本按 `.` 分隔后逐段数值比较。

---

## 私有源认证

插件源支持 **Bearer Token 认证**，用于分发未开源的插件或访问 GitHub 私有仓库中的插件。

### 配置方式

在「管理订阅源」对话框中添加或编辑源时，填入 Token 字段即可。配置了 Token 的源会在列表中显示锁图标。

Token 以 `Authorization: Bearer <token>` 头发送。作用域规则：

- **registry JSON 本身**、**plugins 数组中的所有 URL**、**ZIP 下载** — 始终带 token
- **includes 引用的子源** — 仅与源 URL **同 host** 时带 token，跨域 includes 不带 token（防止泄露）

因此，私有源可以安全地 include 公开的官方源或第三方源，token 不会被发给它们。同时，同服务器上的私有子源（同 host 的 includes）仍能正常认证。

### GitHub 私有仓库

1. 在 GitHub 创建 [Fine-grained Personal Access Token](https://github.com/settings/tokens?type=beta)
2. 权限：只需目标仓库的 **Contents: Read-only**
3. 源 URL 使用 `raw.githubusercontent.com` 格式，与公开仓库相同：
   ```
   https://raw.githubusercontent.com/{用户名}/{私有仓库}/main/registry.json
   ```
4. 在「管理订阅源」中将 PAT 填入 Token 字段

> GitHub PAT 同时适用于 `raw.githubusercontent.com`（registry/plugin.json）和 `github.com`（Release 下载）。Go HTTP 客户端在跨域重定向时会自动剥离 Authorization 头，Release 下载重定向到的 S3 签名 URL 不需要 Token，因此行为正确。

### 自托管私有源

在你的服务器上校验 `Authorization: Bearer <token>` 头即可。以 nginx 为例：

```nginx
location /registry/ {
    if ($http_authorization != "Bearer your-secret-token") {
        return 401;
    }
    root /var/www/plugins;
}
```

### 安全注意事项

- Token 以明文存储在服务端 config 表中（自托管应用，在用户控制范围内）
- `includes` 引用的子源仅在**同 host** 时携带 token，跨域 includes 不会泄露 token
- Token 通过 `Authorization` 请求头发送，不会出现在 URL 中，不会泄露到日志或 Referrer

---

## 注意事项

| 限制 | 值 | 说明 |
|------|-----|------|
| 插件总数上限 | 500 个 | 超出部分截断，记入警告 |
| 递归深度上限 | 20 层 | 超过 20 层的 `includes` 会被跳过 |
| 单个 JSON 大小上限 | 2 MB | 超过则拒绝解析 |
| 单 URL 请求超时 | 15 秒 | 超时后跳过该 URL（记入警告） |
| 循环引用 | 自动检测 | A → B → A 不会死循环，已访问的 URL 自动跳过 |

- `plugins` 中的 URL 必须指向有效的 `plugin.json` 文件
- 如果源文件托管在 GitHub，中国大陆用户可在插件商店中选择 GitHub 镜像加速（预设或自定义），也可在「设置 → 系统 → HTTP 代理」中配置通用代理（如 `http://192.168.1.1:7890`）以加速访问
- 某个 `includes` 或 `plugins` 条目拉取失败不会影响其他源和插件的加载
