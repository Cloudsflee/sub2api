# 管理员方案库

管理员从 `/account-import#selection` 进入方案库。个人记录和历史分析在同一页的另一个标签中。报告记录冻结目录、逐件去向、原文或缺失原因。报告中的采购额以起购批次的历史应付总额为单位。人工费用、按需配套、拟售价与有证据的成交价分别记录。

## 导入报告

沿用管理员认证，接口前缀为 `/api/v1/admin/public-account-import/solutions`。先向 `/reports/validate` 提交报告 JSON，再将同一份 JSON 提交给 `/reports/import`，两者均使用 POST。字段结构见 `backend/internal/service/productselection/solution_report.go`，GPT 场景样例在同目录的 `solution_library_test.go`。商品 ID 必须匹配冻结时的店铺 ID 和商品链接。`snapshot.identity_sha256` 对按字典序排列的 `shop_id/product_id` 以单个 LF 连接后取 SHA-256，末尾无换行。

GET 列表只读取摘要。GET `/:id` 返回方案、证据与当前目录状态，PUT `/:id` 需要提交原 `revision`。版本冲突返回 409。覆盖表、合并记录和逐件去向分别由 `/reports/audit`、`/reports/offers` 提供，均可用 `version` 指定旧报告。重复提交同一版本与内容幂等。同版本内容有变更时需另建版本。每次新报告保存为独立文件，原子替换索引后才对读者可见。报告及编辑索引保存在应用数据目录的 `solution-library` 中。

## 一次性公开详情补读

从生产的已保存商品目录导出 manifest。原子放入 Worker 状态目录的 `product-details.json`。字段包括 `schema: 1`、`enabled: true`、`id`、`snapshot_sha256`、`products`，每个商品须有 `id`、`shop_id`、`goods_key`。运行标识和商品顺序绑定到这一次快照。

常驻 Worker 在空闲通道通过共享探测预算请求公开购买前介绍。`product-details/<id>/status.json` 保存进度，`offers/<商品ID>.json` 保存单件证据。跨进程文件锁覆盖读取、请求与提交。进程重启后从第一件尚未落盘的商品继续。资源受压会等待，公开入口要求验证或持续不可用时会保存原因并停在当前位置。确认公开入口恢复后，在原 manifest 中递增 `resume` 可续跑。把 `enabled` 设为 false 可停止后续领取。
