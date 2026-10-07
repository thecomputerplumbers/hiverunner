import assert from "node:assert/strict"
import { test } from "node:test"
import {
  ALL_RUNNER_SIZES,
  COMPETITOR_RATES,
  FLOOR_RATE,
  RUNNER_SIZES,
  UNRELEASED_RUNNER_SIZES,
  compareMonthlyUsd,
  competitorUsdPerMinuteForLabel,
  runnerArch,
  runnerSize,
  computeUnits,
  priceJob,
  privatePoolUsdPerMinute,
  usdToMicroTokens,
  microTokensToUsd,
} from "../packages/pricing/src/index.ts"

test("minutes are billed whole and never below one", () => {
  assert.equal(computeUnits({ seconds: 0, vcpus: 2, speedScore: 1 }), 2)
  assert.equal(computeUnits({ seconds: 60, vcpus: 2, speedScore: 1 }), 2)
  assert.equal(computeUnits({ seconds: 61, vcpus: 2, speedScore: 1 }), 4)
  assert.equal(computeUnits({ seconds: 600, vcpus: 8, speedScore: 1.5 }), 120)
})
test("a job's tax and earning sum to the charge exactly", () => {
  for (const seconds of [1, 59, 61, 3599, 86400])
    for (const speedScore of [0.7, 1, 1.37, 2.4]) {
      const job = priceJob({ seconds, vcpus: 4, speedScore })
      assert.ok(Number.isInteger(job.amount) && Number.isInteger(job.tax))
      assert.equal(job.tax + job.earning, job.amount)
    }
})
test("rates below the floor are refused", () => {
  assert.throws(() =>
    priceJob({ seconds: 60, vcpus: 2, speedScore: 1, rate: FLOOR_RATE - 1 })
  )
})
test("the placeholders give the documented private pool cost", () => {
  const { gross, net } = privatePoolUsdPerMinute(2)
  assert.equal(gross, 0.001)
  assert.equal(net, 0.00025)
  const job = priceJob({ seconds: 60, vcpus: 2, speedScore: 1 })
  assert.equal(microTokensToUsd(job.amount), 0.001)
  assert.equal(usdToMicroTokens(5), 5_000_000)
})
test("every runner size has a comparison rate on both architectures", () => {
  for (const { vcpus } of ALL_RUNNER_SIZES)
    for (const arch of ["arm64", "x64"])
      for (const provider of ["github", "blacksmith"])
        assert.ok(COMPETITOR_RATES[provider].usdPerMinute[arch][vcpus] > 0)
})
test("the monthly comparison is minutes times the per-minute rates", () => {
  const month = compareMonthlyUsd({ minutes: 100_000, vcpus: 2 })
  assert.equal(month.github, 500)
  assert.equal(month.blacksmith, 250)
  assert.equal(month.gross, 100)
  assert.equal(month.net, 25)
})
test("a label names one size and one architecture", () => {
  const labels = ALL_RUNNER_SIZES.map((size) => size.label)
  assert.equal(new Set(labels).size, labels.length)
  // The labels in use before there was a second architecture stay arm64.
  assert.deepEqual(
    RUNNER_SIZES.map((size) => [size.label, size.vcpus, size.arch]),
    [
      ["hive", 2, "arm64"],
      ["hive-4cpu", 4, "arm64"],
      ["hive-8cpu", 8, "arm64"],
    ]
  )
  assert.deepEqual(
    UNRELEASED_RUNNER_SIZES.map((size) => [size.label, size.vcpus, size.arch]),
    [
      ["hive-x64", 2, "x64"],
      ["hive-x64-4cpu", 4, "x64"],
      ["hive-x64-8cpu", 8, "x64"],
    ]
  )
  assert.equal(runnerSize("hive-x64-4cpu").arch, "x64")
  assert.equal(runnerSize("ubuntu-latest"), undefined)
  assert.equal(runnerSize(null), undefined)
})
test("a job is compared at its own label's architecture", () => {
  assert.equal(competitorUsdPerMinuteForLabel("github", "hive"), 0.005)
  assert.equal(competitorUsdPerMinuteForLabel("github", "hive-x64"), 0.006)
  assert.equal(competitorUsdPerMinuteForLabel("github", "hive-x64-8cpu"), 0.022)
  assert.equal(
    competitorUsdPerMinuteForLabel("blacksmith", "hive-x64-4cpu"),
    0.008
  )
  assert.equal(competitorUsdPerMinuteForLabel("blacksmith", "hive-8cpu"), 0.01)
  assert.equal(competitorUsdPerMinuteForLabel("github", "ubuntu-latest"), null)
})
test("a machine's architecture is read in whichever words it uses", () => {
  for (const said of ["arm64", "aarch64"])
    assert.equal(runnerArch(said), "arm64")
  for (const said of ["x64", "amd64", "x86_64"])
    assert.equal(runnerArch(said), "x64")
  for (const said of ["riscv64", "386", "", null, undefined, 64])
    assert.equal(runnerArch(said), null)
})
