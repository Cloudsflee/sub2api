# Sub2API 自动部署

## 蓝绿热切换

生产安装使用宿主机 HAProxy 监听公网 `8080`，应用槽位只绑定回环地址：

- `sub2api-blue`：`127.0.0.1:18080`
- `sub2api-green`：`127.0.0.1:18081`

正常情况下只有活动槽运行。发布会先在备用槽启动候选镜像，等待 Docker 健康检查和 3
次连续 HTTP `/health` 成功，然后原子生成配置并平滑 reload HAProxy。新请求进入候选槽，
旧 HAProxy worker 继续服务已经建立的连接；旧槽最多排空 10 分钟，超时后才停止。切流后
会继续观察 HAProxy、应用和 Worker 30 秒，期间任一项失败都会热切回旧槽。

首次从直连 `sub2api:8080` 迁移时需要执行一次受控交接，允许约 3 秒端口释放窗口：

```bash
sudo deploy/autodeploy/install.sh --migrate-blue-green
```

安装器会先备份 Compose、HAProxy、脚本、数据库和数据目录，并在 `18081` 预热同镜像
绿槽。失败时会停止 HAProxy、恢复旧 Compose 并重新启动原 `sub2api` 容器。之后的正常
发布不再重建活动槽，也不会因应用发布主动产生 502。

查看当前槽位和切换状态：

```bash
sudo /usr/local/sbin/sub2api-autodeploy-launcher --status
sudo /usr/local/sbin/sub2api-autodeploy-launcher --rollback
```

自动发布会比较上一次成功部署提交的 `backend/migrations`：修改或删除既有迁移，以及新增
迁移中的 `DROP`、`RENAME`、列类型变更或对既有表增加 `NOT NULL` 非空约束会阻止发布。
新建空表可以直接定义非空列。数据库迁移只允许向后兼容的增量变更；回滚镜像不会回滚
已经执行的数据库迁移。

HAProxy 会删除客户端伪造的转发/来源头，再写入真实来源地址。应用只信任 Docker 网关
`172.18.0.1/32`；
Worker 通过 `host.docker.internal:8080` 访问 HAProxy，不依赖具体应用容器名。

健康恢复 timer 每 30 秒检查 HAProxy、活动槽和代理路径，并与发布共用
`/run/lock/sub2api-deploy.lock`。此方案解决发布造成的中断，不提供宿主机宕机级高可用；
首次交接时已经存在的长连接仍可能中断。

该方案使用 GitHub Actions 和服务器端 systemd timer，实现：

1. 本地向 `custom` 分支推送代码。
2. GitHub Actions 完成后端、前端、脚本和 lint 检查。
3. CI 仅在全部成功后将同一个提交快进到 `deploy/custom`。
4. 服务器每 5 分钟检查一次 `deploy/custom`。
5. 服务器使用独立 worktree 构建应用及商品同步 Worker 镜像。
6. 部署前执行数据库和配置备份。
7. 仅重建应用和 Worker，不重启 PostgreSQL/Redis。
8. 健康检查失败时自动恢复上一组镜像。
9. 构建后限制 BuildKit 缓存体积，并为根文件系统保留安全余量。
10. 管理页“立即更新”先同步官方稳定 tag 到 fork，再经过相同 CI 门禁部署。

## 安装

已经完成蓝绿迁移的主机可直接执行：

```bash
cd /opt/sub2api-integration
sudo deploy/autodeploy/install.sh
```

仍使用单实例 `sub2api:8080` 的旧主机必须显式执行首次受控迁移：

```bash
sudo deploy/autodeploy/install.sh --migrate-blue-green
```

安装后不会部署未经 CI 标记的提交。需要立即检查：

```bash
sudo systemctl start sub2api-autodeploy.service
sudo journalctl -u sub2api-autodeploy.service -n 200 --no-pager
```

安装器会先确认 `/opt/sub2api-integration` 是位于 `custom` 分支的 Git 仓库；
条件不满足时不会启用更新定时器，避免管理页接受更新后才发现宿主机没有集成仓库。
更新和部署服务通过固定 launcher 从仓库读取当前 CI 已批准的脚本，因此脚本修复不需要
再次手工复制到 `/usr/local/sbin`。旧安装升级到此机制时需要重新执行一次安装命令。

## 常用命令

```bash
# 查看本地缓存的源提交、CI 批准提交和运行镜像
sudo sub2api-autodeploy-launcher --status

# 刷新远端并查看是否有待部署提交
sudo sub2api-autodeploy-launcher --check

# 部署 CI 批准的提交
sudo sub2api-autodeploy-launcher --deploy

# 只构建 CI 批准的提交，不修改运行容器
sudo sub2api-autodeploy-launcher --build-only

# 强制重新构建/部署当前批准提交
sudo sub2api-autodeploy-launcher --force

# 回退到上一组应用和 Worker 镜像
sudo sub2api-autodeploy-launcher --rollback

# 查看定时器和日志
systemctl status sub2api-autodeploy.timer
journalctl -u sub2api-autodeploy.service -f

# 查看管理页提交的官方版本同步任务
systemctl status sub2api-upstream-sync.timer
journalctl -u sub2api-upstream-sync.service -n 100 --no-pager
```

## 分支职责

- `main`：与官方 `upstream/main` 保持一致。
- `custom`：二开开发和生产代码。
- `deploy/custom`：由 CI 快进维护，表示允许服务器部署的提交；不要手工开发。
- `backup/server-20260713`：首次整理前的服务器代码备份。

## 配置

服务器配置文件为 `/etc/default/sub2api-autodeploy`。默认路径与本服务器一致：

```text
REPO_DIR=/opt/sub2api-custom-src
BUILD_DIR=/opt/sub2api-release
DEPLOY_DIR=/opt/sub2api
PRUNE_BUILD_CACHE=true
BUILD_CACHE_MAX_USED_SPACE=6gb
BUILD_CACHE_MIN_FREE_SPACE=8gb
SYNC_REPO_DIR=/opt/sub2api-integration
UPSTREAM_SYNC_REQUEST_FILE=/opt/sub2api/data/upstream-sync-request
UPSTREAM_SYNC_STATUS_FILE=/opt/sub2api/data/upstream-sync-status
UPSTREAM_SYNC_LOCK_FILE=/run/lock/sub2api-upstream-sync.lock
```

部署状态保存在 `/var/lib/sub2api-autodeploy/state.env`，详细构建日志保存在
`/var/log/sub2api-autodeploy/`。生产 `.env` 只记录当前不可变镜像标签，不会进入 Git。
备份保存在 `/opt/backups/sub2api/`；持续写入的运行日志不会进入恢复归档。
商品目录缓存同样不会进入恢复归档，恢复后由同步 Worker 自动重建。

## 托管版本更新

二开镜像版本格式为 `<官方版本>-custom.<提交前缀>`，例如
`0.1.153-custom.4cf0672931f6`。版本检查只比较前三段官方版本号，因此相同
官方基线不会误报更新，新的官方稳定版本仍会正常提示。

托管构建不会在容器内下载或替换官方二进制。管理页按钮显示为“同步到我的仓库”，
并持久展示 `queued`、`processing`、`pushed` 或 `failed` 宿主机状态。管理员点击后：

1. 后端原子写入只包含 `vX.Y.Z` 的更新请求。
2. `sub2api-upstream-sync.timer` 在宿主机读取请求并持有独立的仓库同步锁。
3. 同步脚本确认集成仓库干净、tag 属于官方 `main`，且 fork 的 `main` 可以快进。
4. 脚本先在本地把该 tag 合并到 `custom`；冲突时会记录具体冲突文件并恢复工作区。
5. 合并成功后，使用一次原子 push 同时推进 fork 的 `main`、`custom` 并创建
   `upstream/vX.Y.Z` 追踪 tag；不会推送 `v*` tag 误触发 fork 的 Release workflow。
6. CI 全绿后推进 `deploy/custom`，服务器按正常备份、健康检查和回滚流程部署。

合并冲突、非快进、工作区修改或网络错误都会终止同步；此时不会推进
`custom` 或 `deploy/custom`，当前生产容器保持不变。托管构建的版本回退继续使用
服务器保存的不可变镜像，不使用官方 Release 二进制。部署端会显式同步 fork 的
正式 tags；中断或推送失败时，集成仓库会恢复到远端 `custom`，请求可安全重试。
