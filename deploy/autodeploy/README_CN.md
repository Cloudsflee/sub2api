# Sub2API 自动部署

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

```bash
cd /opt/sub2api-integration
sudo deploy/autodeploy/install.sh
```

安装后不会部署未经 CI 标记的提交。需要立即检查：

```bash
sudo systemctl start sub2api-autodeploy.service
sudo journalctl -u sub2api-autodeploy.service -n 200 --no-pager
```

安装器会先确认 `/opt/sub2api-integration` 是位于 `custom` 分支的 Git 仓库；
条件不满足时不会启用更新定时器，避免管理页接受更新后才发现宿主机没有集成仓库。

## 常用命令

```bash
# 查看本地缓存的源提交、CI 批准提交和运行镜像
sudo sub2api-autodeploy --status

# 刷新远端并查看是否有待部署提交
sudo sub2api-autodeploy --check

# 部署 CI 批准的提交
sudo sub2api-autodeploy --deploy

# 只构建 CI 批准的提交，不修改运行容器
sudo sub2api-autodeploy --build-only

# 强制重新构建/部署当前批准提交
sudo sub2api-autodeploy --force

# 回退到上一组应用和 Worker 镜像
sudo sub2api-autodeploy --rollback

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
4. 脚本先将对应提交推送到 `Cloudsflee/sub2api` 的 `main`，并创建
   `upstream/vX.Y.Z` 追踪 tag；不会推送 `v*` tag 误触发 fork 的 Release workflow。
5. 再把该 tag 合并到 `custom` 并推送，从而触发 GitHub CI。
6. CI 全绿后推进 `deploy/custom`，服务器按正常备份、健康检查和回滚流程部署。

合并冲突、非快进、工作区修改或网络错误都会终止同步；此时不会推进
`custom` 或 `deploy/custom`，当前生产容器保持不变。托管构建的版本回退继续使用
服务器保存的不可变镜像，不使用官方 Release 二进制。部署端会显式同步 fork 的
正式 tags；中断或推送失败时，集成仓库会恢复到远端 `custom`，请求可安全重试。
