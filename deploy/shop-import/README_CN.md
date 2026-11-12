# 候选店铺分批导入

在已核实的运维宿主机运行 Python 3 控制器。它仅通过现有 `POST /shops` 登记，使用
`GET /products/sync-control` 观察调度；**不请求 `/products/sync-job`**，不额外催采。
原有普通、可信、不可信基础周期为 60、15、240 分钟，统一倍率作用于全部店铺。
AI 标签仅排序，新店仍使用普通等级。单店 1,000 件上限与完整快照校验保持不变。

## 执行与续跑

先核实项目 HAProxy 活动槽与 `172.93.218.157:8080` 运维入口。宿主机通过回环地址调用
API，凭据从当前活动容器读取到进程内存，不写入日志、状态文件或命令行。自动发布定时器保持停止。

```bash
python3 controller.py preview \
  --source /path/to/wzyp-shops-2026-11-04.csv \
  --target http://127.0.0.1:8080 \
  --environment production:172.93.218.157:8080 \
  --expected-count 100 --state /path/to/run
python3 controller.py run --state /path/to/run
python3 controller.py report --state /path/to/run
```

`preview` 冻结原文件、SHA-256、候选列表和原有店铺集合。再次执行同一预览不会重建候选列表；
源文件或目标改变时拒绝覆盖。运行中断后执行同一个 `run` 命令，先按平台标识核对线上资料，
成功 POST 的响应丢失也不会重复建店。每个写入前保存意图和续期租约；同一状态目录有本地锁，
同一后端有 180 秒租约与版本比较，其他控制器不能并行运行。崩溃后的租约到期后可续跑。

在经过完整周期验证的规模边界停下后，显式继续剩余**未提交**候选：

```bash
python3 controller.py run --state /path/to/run --continue-after-boundary
```

这会重新采集 20 分钟基线并从两家批次开始；已隔离店铺仍保持隔离。异常店铺需先根据
`report.json` 的错误及源行核查，随后用控制接口定点解除隔离。店铺与商品资料始终保留。

## 观测与控制

- 每分钟记录宿主 CPU、可用内存、磁盘、容器工作集、限额、OOM、重启标识、Worker 通道与压力事件。
  工作集使用容器内存减去 inactive file cache；CPU 用相邻 `/proc/stat` 计数计算。
- 后端按实际调度间隔、失败退避、活跃租约判断 `automatic_due`；`automatic_eligible` 另外扣除
  暂停和隔离。原有店铺只按冻结的 ID 集合统计，基线采用 20 分钟到期待采数的最大值。
  `pending_shops` 和快照过期数不用于判断积压。
- 基线至少 20 分钟；每批按 2 → 5 → 10 家增长，至多两家等待首采；批后至少 15 分钟连续正常。
  任意重启或超过 90 秒的观测空档重置连续窗口，并增加 20 分钟稳定等待。
- 正常要求无新增上游压力，至少五条可用通道，工作集低于 90%，宿主可用内存至少 20%，
  CPU 不高于 85%，原有店铺到期待采数不超过基线加六家且不持续增长。
- 上游压力立即停止新增；工作集达到 90%、通道少于五条或 CPU 超过 85% 持续五分钟，
  或积压连续十分钟增长，也停止新增。恢复后下一批减半，最少两家。
- 新增 OOM、连续两次工作集达到 95%、宿主可用内存低于 10%、磁盘低于 1 GiB，
  或 Worker 状态超过两分钟未更新时暂停自动派单。观测失效同样停止新增并尝试暂停。
  恢复需连续 15 分钟工作集低于 88%，且通道、队列、宿主内存恢复。
- 仅请求压力或积压在停止新增后持续 20 分钟才将全店倍率依次升至 2、4。
  内存与 CPU 压力不触发降频。4 倍仍不稳定时从最近批次开始隔离，停止剩余候选接入。
- 两次连续完整任务失败或首采超过 60 分钟未确认则隔离；空目录、无可售商品、明确关闭与失败分开记录。
  后端持久化任务成功/失败计数，避免采样间隔内多次失败漏算；过期租约重领计入失败。
- 接入结束后至少观察一个普通店铺当前周期。若降过频，逐档尝试恢复，每档观察完整周期；
  较快档出现压力则回到已验证档并重新观察。未完成周期不会标记 `finished`。

## 控制接口

`GET/PUT /api/v1/public/account-import/products/sync-control` 使用现有 Worker Bearer 凭据。
GET 返回 `control`、只读店铺调度/首采/失败统计和经过脱敏的 `worker` 状态，不领取任务。
PUT 支持部分配置替换，必须带 GET 返回的 `expected_revision`：

```json
{
  "expected_revision": 0,
  "interval_multiplier": 1,
  "automatic_paused": false,
  "quarantined_shops": {},
  "controller_id": "operator-controller",
  "lease_seconds": 180
}
```

倍率仅允许 1、2、4。隔离清单为内部店铺 ID → 原因，替换时保留其他已有项。
租约可为 30–180 秒；持有者用 0 释放。配置落在商品缓存同目录下的
`public-account-import-sync-control.json`，使用临时文件、fsync 和原子替换；保存失败保留原配置。
缺省倍率 1、正常派单、无隔离。暂停和隔离只影响新的自动整店派单；手动请求、在途心跳和提交保留原规则。

运行产物为 `state.json`、`report.json`、`samples.jsonl`、冻结 CSV 及候选/原有店铺清单。
报告按登记、完整首采、有商品、空目录、关闭、异常隔离、压力隔离、未提交分别统计，保留批次耗时与事件。
这些运行产物不提交仓库。发布、备份和回退复用 [本项目发布约定](../autodeploy/README_CN.md)。

验证：`python3 -m unittest -v test_controller`；Go 运行受影响的 handler、routes 测试及一次 vet。
