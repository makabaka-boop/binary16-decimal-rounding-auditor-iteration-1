# halfconv — 精确十进制 → IEEE binary16 批处理转换器

把仪器测得的十进制字符串批次转换为 IEEE 754-2008 **binary16（半精度）**
位型。整个转换只用 Go 的整数与有理数运算（`math/big.Int` /
`math/big.Rat`），**不经过宿主浮点**，因此：

- 不会出现“先转 `double` 再强转 half”在居中点上的二次舍入；
- 极小值也不会在不可见的二进制舍入中悄悄带上非预期的符号——
  `-0` 与 `+0` 都按输入字面量原样保留为带符号零。

## 输入 / 输出

请求是 JSON，支持两种外形：

```json
{ "values": ["0.1", "-0", "65520", "1e-50"] }
```

或直接一个字符串数组：

```json
["0.1", "-0", "65520", "1e-50"]
```

约束：

- 每批 **1～1000** 个字符串；
- 每个值最多 **30 位有效数字**（前导零不计）；
- 十进制指数限于 **-50～50**（含端点）；
- 只接受普通有限十进制字面量；`NaN`、`Infinity`、`inf`、
  十六进制、下划线、空字符串等一律拒绝。

每个元素独立处理，单个非法值不会中断整批：

```json
{
  "count": 4,
  "results": [
    {
      "index": 0,
      "input": "0.1",
      "ok": true,
      "hex": "2E66",
      "class": "normal",
      "rounding_error": "-1/40960"
    },
    {
      "index": 1,
      "input": "-0",
      "ok": true,
      "hex": "8000",
      "class": "zero",
      "rounding_error": "0/1"
    },
    {
      "index": 2,
      "input": "65520",
      "ok": true,
      "hex": "7C00",
      "class": "infinity",
      "rounding_error": null
    },
    {
      "index": 3,
      "input": "NaN",
      "ok": false,
      "error": "invalid decimal format"
    }
  ]
}
```

- `hex`：4 位十六进制 binary16 位型；
- `class`：`zero` / `subnormal` / `normal` / `infinity`；
- `rounding_error`：**舍入值 − 原值** 的最简分数（`big.Rat` 天然约分）；
  结果为无穷时是 `null`；被拒绝的元素不含该字段。

## 舍入规则

IEEE binary16，**roundTiesToEven**（最近值，正好居中取偶数）：

- 正规区按 binade 定位后，把尾数放大成整数，用整数商/余数比较
  `2·余数` 与分母，决定下取、上取或偶舍入，全程不出现浮点；
- 次正规区间距为 2⁻²⁴，居中点 2⁻²⁵ 在零（偶数）与最小次正规之间，
  偶舍入到零；
- 边界居中点 2⁻¹⁴·(2047/2) 在最大次正规（奇数）与最小正规（偶数）
  之间，偶舍入到最小正规；
- 最大有限值 65504 与 +∞ 的居中点 **65520** 偶舍入到无穷（无穷候选的
  尾数字段为 0，是偶数）。

## 运行

本地：

```sh
go test ./...
go build ./cmd/halfconv
./halfconv data/batch.json
cat data/batch.json | ./halfconv
```

Docker Compose（`half` 服务负责批量运行；镜像构建阶段会跑完整测试，
包括全部 63488 个有限位型的穷举往返）：

```sh
docker compose up --build half             # 转换挂载的 /data/batch.json
docker compose run --rm half /data/my.json
cat batch.json | docker compose run --rm -T half -
```

## 目录

| 路径 | 作用 |
| --- | --- |
| `internal/decimal` | 十进制字符串 → 精确 `big.Rat`（含 30 位/指数/格式校验） |
| `internal/half` | binary16 纯整数/有理数舍入与位型重建 |
| `internal/app` | JSON 批处理装配与命令行入口逻辑 |
| `cmd/halfconv` | 命令行 |
| `data/batch.json` | 示例批次（含边界值与非法值） |

## 测试

- `TestAllFinitePatternsRoundTrip`：穷举全部 **2 × 31 × 1024 = 63488**
  个有限 binary16 位型——精确位型值渲染成十进制、解析、再舍入，必须
  还原同一位型、同一分类与零误差，正负零分别覆盖；
- 次正规边界（2⁻²⁵ 居中、2⁻¹⁴ 跨界居中、最大/最小次正规）；
- 正规居中点（1+2⁻¹¹、1.5+2⁻¹¹、顶 binade 的 65488）与溢出
  （65504 / 65520 / ±1e50）；
- 正负零、`0.1`（误差精确为 −1/40960）、极小值 1e-50 的有符号零；
- 解析器对 NaN/Infinity/非法格式/超位数/超指数的拒绝；
- 另含一个**完全独立的暴力 oracle**：枚举所有 binary16 候选直接比较
  有理数距离做 tie-to-even，与生产实现在数千个二进制网格点（含大量
  精确居中点）和随机十进制串上对拍。
