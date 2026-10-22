# leopic 设计文档

> 版本 v1.0（2026-10-22）。本文是实现、迁移、插件改造的唯一参考。
> 目标：替代已停更的 lsky-pro V2.1，单用户、兼容其上传 API、老直链零改动。

## 1. 范围

要做：
- 兼容 lsky 的 `/api/v1/*`（上传 / 列表 / 删除 / token / profile），老脚本不改即可用。
- 网页管理端：拖拽 / 粘贴 / 选择上传、列表、搜索、删除、复制链接、token 管理。
- 服务端为每张图生成一张 WebP 衍生图（替代插件本地压缩 + lsky 的 PNG 缩略图）。
- 记录级去重：同内容重复上传直接返回已有记录。
- 从 lsky 的 SQLite 一次性导入（含孤儿文件、旧 token），文件结构不变。

不做：多用户 / 注册、相册、游客上传、`/s/{key}` 短链（lsky 2.1 源码里不存在该路由）、原图保护 `/{key}.{ext}`、防盗链（交给 Cloudflare）、上传时改写原图（压缩 / 水印 / 转格式）。

## 2. 架构

```
                 OpenResty
GET /img/*   ──► alias  {DATA}/uploads/      原图直链（应用不参与）
GET /i/*     ──► alias  {DATA}/uploads/      lsky 默认前缀，兜底
GET /webp/*  ──► alias  {DATA}/webp/         WebP 衍生图
其余         ──► proxy  leopic:8080           上传 / API / 管理页
```

- 单个 Go 二进制，`CGO_ENABLED=0`，镜像 distroless/static，非 root（uid 65532）。
- 依赖：`modernc.org/sqlite`（纯 Go SQLite）、`github.com/gen2brain/webp`（WebP 编解码，build tag `nodynamic`）、`golang.org/x/image`（bmp/tiff 解码、缩放）。
- 应用自身也能服务 `/img/` `/i/` `/webp/`（便于本地开发、OpenResty 未配置时兜底），生产环境由 OpenResty 直出。
- 时区固定 `Asia/Shanghai`（与 lsky 的 PRC 一致），二进制内嵌 tzdata。

## 3. 配置（环境变量）

| 变量 | 默认 | 说明 |
|---|---|---|
| `LEOPIC_ADDR` | `:8080` | 监听地址 |
| `LEOPIC_DATA` | `/data` | 数据根目录 |
| `LEOPIC_UPLOADS` | `{DATA}/uploads` | 原图目录（即 lsky 的 `storage/app/uploads`） |
| `LEOPIC_WEBP_DIR` | `{DATA}/webp` | 衍生图目录 |
| `LEOPIC_DB` | `{DATA}/leopic.db` | SQLite 文件 |
| `LEOPIC_BASE_URL` | 必填 | 如 `https://pic.leoreo.me`，生成链接用 |
| `LEOPIC_IMG_PREFIX` | `/img` | 原图 URL 前缀 |
| `LEOPIC_WEBP_PREFIX` | `/webp` | 衍生图 URL 前缀 |
| `LEOPIC_PASSWORD` | 必填 | 管理端登录密码，也用于 `POST /api/v1/tokens` |
| `LEOPIC_EMAIL` | 空 | 设置后 `POST /api/v1/tokens` 要求 email 匹配 |
| `LEOPIC_ADMIN_PATH` | 空 | 后台隐藏：安全地址（如 `/knock-x7k9`）。访问该地址种 cookie 解锁；未解锁时页面与 API 一律 301 到 `LEOPIC_ADMIN_REDIRECT`。图片直链（`/img/` `/i/` `/webp/`）与 `/healthz` 不受影响。留空则不隐藏（本地开发） |
| `LEOPIC_ADMIN_REDIRECT` | 空 | 未解锁时的 301 跳转目标（如 `https://leoreo.me`），设 `ADMIN_PATH` 时必填。跳转带 `Cache-Control: no-cache`，解锁后浏览器不会被钉死的 301 卡住 |
| `LEOPIC_MAX_SIZE_MB` | `50` | 单文件上限 |
| `LEOPIC_MAX_PIXELS` | `50000000` | 像素上限（防解压炸弹） |
| `LEOPIC_WEBP_QUALITY` | `82` | WebP 质量 |
| `LEOPIC_WEBP_MAX_EDGE` | `2560` | 衍生图最长边，超过才缩，不放大 |

## 4. 存储与命名

- 原图：`{UPLOADS}/YYYY/MM/DD/{13位hex}.{ext}`，URL `{BASE}{IMG_PREFIX}/YYYY/MM/DD/{name}`。
- 衍生图：`{WEBP_DIR}/YYYY/MM/DD/{13位hex}.webp`，URL `{BASE}{WEBP_PREFIX}/YYYY/MM/DD/{stem}.webp`。
  - 衍生图路径由原图 `path` + `name` 去掉扩展名得到，原图本身是 webp 也不冲突（分目录）。
- 13 位 hex 沿用 PHP `uniqid()`：`%08x` 秒 + `%05x` 微秒，日期目录按 Asia/Shanghai。以 `O_EXCL` 创建，冲突则微秒 +1 重试。
- `key`：6 位 `[A-Za-z0-9]`，crypto/rand，唯一索引冲突则重试。
- 扩展名按内容嗅探决定：`jpg png gif webp bmp tiff`，不信任客户端文件名。其余格式拒绝（`不支持的文件类型`）。

## 5. 衍生图（WebP）

- 上传时同步生成，响应返回时已可访问。
- 解码 → 按 EXIF Orientation 摆正（JPEG）→ 最长边 > `WEBP_MAX_EDGE` 时 CatmullRom 等比缩放 → WebP 有损编码（quality 82，method 4），保留透明通道。
- GIF 不生成衍生图（保动画），`webp` 相关链接回落为原图。
- 衍生图不含 EXIF（顺带去掉 GPS 等隐私信息）；原图原样保存。
- 编码并发限制为 2，避免内存峰值。
- `leopic regen [--all]`：补生成缺失的衍生图；`--all` 强制全部重建（调参后用）。

## 6. 数据库

```sql
CREATE TABLE images (
  id          INTEGER PRIMARY KEY,
  key         TEXT    NOT NULL UNIQUE,
  path        TEXT    NOT NULL,            -- "2026/10/22"
  name        TEXT    NOT NULL,            -- "6ad5c7246776a.png"
  origin_name TEXT    NOT NULL DEFAULT '',
  size        INTEGER NOT NULL,            -- 字节
  mimetype    TEXT    NOT NULL,
  extension   TEXT    NOT NULL,
  md5         TEXT    NOT NULL,
  sha1        TEXT    NOT NULL,
  sha256      TEXT    NOT NULL UNIQUE,     -- 去重依据
  width       INTEGER NOT NULL DEFAULT 0,
  height      INTEGER NOT NULL DEFAULT 0,
  webp_size   INTEGER NOT NULL DEFAULT 0,  -- 0 = 无衍生图
  created_at  INTEGER NOT NULL,            -- Unix 秒
  UNIQUE (path, name)
);
CREATE INDEX images_created ON images(created_at);

CREATE TABLE tokens (
  id           INTEGER PRIMARY KEY,
  name         TEXT    NOT NULL DEFAULT '',
  token_hash   TEXT    NOT NULL UNIQUE,    -- sha256(plain) hex，与 Sanctum 一致
  created_at   INTEGER NOT NULL,
  last_used_at INTEGER
);

CREATE TABLE sessions (
  id_hash    TEXT PRIMARY KEY,             -- sha256(cookie 值)
  expires_at INTEGER NOT NULL
);
```

## 7. 鉴权

- API token（Sanctum 兼容）：`Authorization: Bearer {id}|{plain40}`。含 `|` 时按 id 取记录、常量时间比较 `sha256(plain)`；不含 `|` 时按 hash 查。成功后更新 `last_used_at`。
- 管理端 session：`POST /login` 校验 `LEOPIC_PASSWORD`，下发 `leopic_session` cookie（HttpOnly、Secure（BASE_URL 为 https 时）、SameSite=Strict、30 天），服务端只存 hash。
- 所有 `/api/*`（除 `POST /api/v1/tokens`）接受 token 或 session 任一。
- 不支持游客：无凭据一律 401。
- 登录与 `POST /api/v1/tokens` 共用全局失败限流：每分钟最多 5 次失败。
- 后台隐藏（`LEOPIC_ADMIN_PATH` 设置时生效）：入口是最外层 `gate` 中间件。访问安全地址 → 下发 `leopic_admin` cookie（HttpOnly、Strict、30 天、值每次启动随机）→ 302 回 `/`；未解锁访问任何非公开路径（页面、静态资源、`/api/*`、`/login`）一律 301 到 `LEOPIC_ADMIN_REDIRECT`。公开路径仅图片直链与 `/healthz`，博客等外链不受影响。

## 8. API

### 8.1 通用约定（照抄 lsky）

- 响应 `{"status": bool, "message": string, "data": object}`，失败时 `data` 为 `{}`。
- 业务错误 HTTP 200 + `status:false`；未认证 401（`Unauthenticated.`）；限流 429（`Too Many Attempts.`）。
- `size` 对外单位为 KB（float），库内为字节。

### 8.2 `POST /api/v1/upload`

multipart 字段 `file`（必填）；`strategy_id` 等其他字段接受但忽略。

成功（含去重命中）：
```json
{
  "status": true,
  "message": "上传成功",
  "data": {
    "key": "aB3dE9",
    "name": "6ad5c7246776a.png",
    "pathname": "2026/10/22/6ad5c7246776a.png",
    "origin_name": "shot.png",
    "size": 52.0947265625,
    "mimetype": "image/png",
    "extension": "png",
    "md5": "…",
    "sha1": "…",
    "links": {
      "url": "https://pic.leoreo.me/img/2026/10/22/6ad5c7246776a.png",
      "html": "<img src=\"…\" alt=\"shot.png\" title=\"shot.png\" />",
      "bbcode": "[img]…[/img]",
      "markdown": "![shot.png](…)",
      "markdown_with_link": "[![shot.png](…)](…)",
      "thumbnail_url": "https://pic.leoreo.me/webp/2026/10/22/6ad5c7246776a.webp",
      "webp": "https://pic.leoreo.me/webp/2026/10/22/6ad5c7246776a.webp",
      "webp_markdown": "![shot.png](https://pic.leoreo.me/webp/…webp)"
    },
    "width": 1600, "height": 900, "webp_size": 81.2, "duplicate": false
  }
}
```
- `links` 前 6 个键与 lsky 完全一致（`html` 为真实 HTML 标签，JSON 编码器关了 EscapeHTML，`<` `>` 原样输出；曾误写成 `&lt;` 字面量已修正）；`webp`、`webp_markdown`、`width`、`height`、`webp_size`（KB）、`duplicate` 为扩展字段。
- 无衍生图时 `thumbnail_url`、`webp` 等于 `url`。
- 去重命中返回已有记录，`duplicate: true`。
- 错误 message：`请选择要上传的图片`、`不支持的文件类型`、`图片大小超出限制`、`图片尺寸超出限制`、`服务异常，请稍后再试`。

### 8.3 `GET /api/v1/images`

query：`page`（默认 1）、`order`（`newest` 默认 / `earliest` / `utmost` / `least`）、`keyword`（匹配 origin_name、name、key）、`per_page`（扩展，默认 40，最大 100）。`permission`、`album_id` 忽略，返回全部。

`data` 为 Laravel 分页结构：`current_page, data[], first_page_url, from, last_page, last_page_url, next_page_url, path, per_page, prev_page_url, to, total`。单项字段：`key, name, origin_name, size, mimetype, extension, md5, sha1, width, height, human_date, date, pathname, links, webp_size`。

### 8.4 其他兼容接口

| 方法 | 路径 | 行为 |
|---|---|---|
| DELETE | `/api/v1/images/{key}` | 删记录 + 原图 + 衍生图；key 不存在也返回 `删除成功` |
| POST | `/api/v1/tokens` | `email`、`password` → `{"token":"{id}|{plain}"}`，失败 `The email address or password is incorrect.` |
| DELETE | `/api/v1/tokens` | 删除全部 token |
| GET | `/api/v1/profile` | `name, email, avatar:"", capacity:0, used_capacity(KB), url, image_num, album_num:0, registered_ip:""` |
| GET | `/api/v1/strategies` | `{"strategies":[{"id":1,"name":"本地"}]}` |
| GET | `/api/v1/albums` | 空分页 |

### 8.5 管理扩展接口

| 方法 | 路径 | 行为 |
|---|---|---|
| POST | `/login` | JSON `{"password"}`，成功下发 cookie |
| POST | `/logout` | 清除 session |
| GET | `/api/admin/tokens` | `[{id,name,created_at,last_used_at}]` |
| POST | `/api/admin/tokens` | `{"name"}` → `{"token":"id|plain"}`（明文只返回这一次） |
| DELETE | `/api/admin/tokens/{id}` | 吊销 |
| POST | `/api/admin/images/delete` | JSON `{"keys":[...]}`，单事务批量删除，返回 `{"deleted":n}`；上限 1000 |
| POST | `/api/admin/download` | 表单 `key`（可重复）+ `variant`（`original`/`webp`）。单张直接返回文件，多张流式 zip（Store 不压缩）；文件名用原始文件名，重名追加 ` (2)` |
| GET | `/api/admin/session` | 登录态检查，返回 `base_url` |
| GET | `/healthz` | `ok` |

## 9. 管理端

内嵌单页（`embed.FS`，原生 HTML/CSS/JS，无框架、无外部资源），浅色 / 深色跟随系统。

- 登录页：只有密码框。
- 顶栏：搜索框、设置。
- 上传区：拖拽 / 粘贴 / 点选，多文件顺序上传，显示进度与结果。
- 图片网格：WebP 预览（`loading="lazy"`），文件名、日期、大小；“加载更多”分页。
- 详情抽屉：大图预览、尺寸 / 大小 / 原名 / 日期；一键复制 原图 URL、原图 Markdown、WebP URL、WebP Markdown、HTML；删除（二次确认）。
- 设置：token 列表 / 新建 / 吊销；默认复制格式（localStorage）。
- 所有用户内容（文件名等）一律以 textContent 渲染，防 XSS。

## 10. 命令行

```
leopic serve                     # 默认
leopic import --lsky-db FILE [--dry-run] [--no-orphans]
                 [--drop-compressed] [--redirect-map FILE]   # 剔除有原图配对的 -compressed 压缩图，输出 nginx 301 映射
leopic regen [--all]
leopic token create [NAME]       # 打印 id|plain
```

## 11. 从 lsky 迁移

### 11.1 lsky 事实（源码 + 实盘核对）

- 文件：宿主机 `/opt/1panel/apps/lsky-pro/lsky-pro/data/storage/app/uploads/YYYY/MM/DD/{uniqid}.{ext}`；`public/img` 是指向它的 symlink，策略 URL `https://pic.leoreo.me/img`。
- `images` 表：直链相对路径 = `path + "/" + name`；`size` 为 KB；`created_at` 为 `Y-m-d H:i:s` 的 PRC 本地时间；`origin_name` / `alias_name`（显示名 = `alias_name ?: origin_name`）；无软删除。
- lsky 文件级去重、记录不去重：785 行 → 694 个文件，多行可指向同一 `path/name`。
- `personal_access_tokens`：`id`、`token`（sha256 hex）、`name`、`created_at`、`last_used_at`。
- `public/jpg`、`public/png` 两个无扩展名文件是事故产物，不在 uploads 内，不迁。
- 旧缩略图 `public/thumbnails/` 不迁，由衍生图替代。
- **压缩图实况（2026-10-22 实盘核对）**：utools-lsky 1.0 时代每张图双上传（原图 + `{stem}-compressed.{jpg|webp}`，canvas JPEG q75），215 个压缩图文件中 213 个有原图配对（共约 45MB）；typecho 博客 272 个图床引用中 81 个指向压缩图 URL。2 个压缩图（`20260525_蓝冰的设定压缩图_AI绘画,Furry-compressed.jpg`、`ep00-cover-watermarked-compressed.webp`）本就只有这一份，是正常图片，照常导入。
- 服务器 uploads 比库多 31 个孤儿（2025/09/06-07 批量 PNG）+ 1 个 `.gitignore`（非图片，忽略）。

### 11.2 导入逻辑（`leopic import`，幂等）

1. 只读打开 lsky DB。
2. 按 `(path, name)` 分组：取 `created_at` 最早的一行的 `key`、显示名、时间。
3. `--drop-compressed`：剔除有原图配对的压缩图（见 11.2.1），可配 `--redirect-map FILE` 输出 nginx 301 映射。
4. 文件不存在 → 警告跳过；`(path,name)` 已在新库 → 跳过；sha256 已存在 → 跳过（文件仍在盘，直链不受影响）。
5. 读文件重算 size / md5 / sha1 / sha256 / 宽高 / mimetype，生成衍生图，入库。
6. 孤儿文件（`--no-orphans` 关闭）：遍历 `{UPLOADS}`，不在库的图片按同样流程入库；`origin_name` = 文件名；时间优先从 13 位 uniqid 前 8 位解出，否则用文件 mtime；`key` 新生成。
7. 导入 `personal_access_tokens`（同 id、同 hash；id 已存在则跳过），旧 token 继续可用。
8. 打印汇总：导入 / 孤儿 / 剔除压缩图 / 跳过 / 缺失 / 失败 / token 数。

#### 11.2.1 压缩图配对规则（`splitCompressed`）

- 识别：显示名（`alias_name ?: origin_name`）匹配 `(?i)^(.+)-compressed\.(jpg|jpeg|png|webp|gif)$`。
- 配对：stem 相同 + 同日期目录 + 同宽高；多候选（如通名 `image.png`）按文件名 uniqid 前 8 位（上传秒）就近消歧（≤60s）；仍无唯一候选则保留压缩图不剔。
- 剔除只影响导入清单与 map 输出，**不动磁盘文件**；文件删除在迁移时显式做（备份后）。
- map 目标：原图衍生图 `{WEBP_PREFIX}/{dir}/{stem}.webp`；GIF 原图无衍生图，回落原图 URL。
- 实测（2026-10-22）：剔除 213 对，与独立 Python 分析逐条一致。

### 11.3 上线步骤（对齐部署便签：端口 18085、静态目录在 www 树内）

```bash
# 0. 备份（lsky 无任何全量备份，动手前必做）
mkdir -p /opt/1panel/apps/lsky-pro/backups
tar -C /opt/1panel/apps/lsky-pro/lsky-pro/data -czf /opt/1panel/apps/lsky-pro/backups/lsky-$(date +%F).tgz database storage/app/uploads

# 1. 数据落位：uploads 取代 www 树内的 451MB 快照（OpenResty 只挂载 /opt/1panel/www）
LSKY_UP=/opt/1panel/apps/lsky-pro/lsky-pro/data/storage/app/uploads
WWW=/opt/1panel/www/sites/pic.leoreo.me
MAP=/opt/1panel/apps/imgbed/redirect.map.conf   # 本仓库 docs/migration/ 有生成样例
#   剔除 213 个已配对压缩图（map 左列即清单；root 的 rm 有 -i 别名，必须 command rm）
grep -oE '^    /img/\S+' $MAP | sed 's|^    /img/||' | while read f; do command rm -f "$LSKY_UP/$f" "$WWW/img/$f"; done
mv $LSKY_UP/* $WWW/img/             # 与快照同内容，合并后快照副本消失
mkdir -p $WWW/webp
chown -R 65532:65532 $WWW/img $WWW/webp

# 2. 部署（compose 绑 127.0.0.1:18085，见 docker-compose.yml）
cd /opt/1panel/apps/imgbed && docker compose up -d --build

# 3. 导入 + 衍生图
docker compose exec -v /opt/1panel/apps/lsky-pro/lsky-pro/data/database/database.sqlite:/lsky.sqlite:ro leopic \
  import --lsky-db /lsky.sqlite --drop-compressed --redirect-map /data/redirect.map.conf

# 4. 切 OpenResty（见 11.4）→ reload → CF Purge Everything

# 5. 验收：694 条记录中 481 个文件 HEAD 200；81 个博客引用的压缩图 URL 301 → webp 200
```

### 11.4 OpenResty 片段（`pic.leoreo.me/proxy/root.conf`）

```nginx
# http 级 include（1Panel 的 nginx.conf 主配置或 conf.d 公共段）：
#   include /opt/1panel/www/sites/pic.leoreo.me/proxy/redirect_map.conf;

location ^~ /img/ {
    if ($leopic_redirect) { return 301 $leopic_redirect; }   # 旧压缩图直链 → 原图衍生图
    alias /www/sites/pic.leoreo.me/img/;                     # = /opt/1panel/www/...
    expires 30d;
    add_header Cache-Control "public, max-age=2592000";
    access_log off;
}
location ^~ /i/    { alias /www/sites/pic.leoreo.me/img/;  expires 30d; add_header Cache-Control "public, max-age=2592000"; }
location ^~ /webp/ { alias /www/sites/pic.leoreo.me/webp/; expires 30d; add_header Cache-Control "public, max-age=2592000"; access_log off; }
location / {
    client_max_body_size 60m;
    proxy_pass http://127.0.0.1:18085;
    proxy_set_header Host $host;
    proxy_set_header X-Forwarded-Proto $scheme;
}
```

- 改完 `docker exec 1Panel-openresty-Za8k nginx -t && docker exec 1Panel-openresty-Za8k nginx -s reload`。
- 删除图片后 Cloudflare 可能仍缓存旧直链，需要时在 CF 手动 purge；上线切换后建议 Purge Everything（维护期间边缘可能缓存过维护页）。
- 回滚：`cat root.conf.orig > root.conf` + reload（便签 §3.2）。

## 12. utools-lsky 插件改造

- 每张图只上传一次原图；“压缩图”链接改取响应 `links.webp` / `links.webp_markdown`。
- 去掉本地 Canvas 压缩及相关设置（质量、格式、最长边、压缩后缀、上传原图 / 压缩图开关）。
- 历史记录、CSV 导入导出字段保持不变（`compressed_*` 填 webp 信息），旧记录可继续导入。
- 去掉“游客模式”说法：未配置 token 时提示先配置。
- `lsky_batch_upload.py` 同步：去掉 Pillow 压缩与 `--mode/--quality/--format` 等参数，记录 `links.webp`。
- 版本号升到 1.2.0，`plugin.json` / `VERSION` / `README.md` 对齐。
