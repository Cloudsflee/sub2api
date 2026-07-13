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
```

部署状态保存在 `/var/lib/sub2api-autodeploy/state.env`，详细构建日志保存在
`/var/log/sub2api-autodeploy/`。生产 `.env` 只记录当前不可变镜像标签，不会进入 Git。
