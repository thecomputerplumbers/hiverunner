import assert from "node:assert/strict"
import { test } from "node:test"
import {
  ALL_RUNNER_SIZES,
  compareMonthlyUsd,
  COMPETITOR_RATES,
  competitorUsdPerMinuteForLabel,
  computeUnits,
  FLOOR_RATE,
  MACOS_RUNNER_SIZES,
  machineServes,
  microTokensToUsd,
  OWNER_CORES,
  priceJob,
  privatePoolUsdPerMinute,
  RUNNER_SIZES,
  runnerArch,
  runnerSize,
  TOP_UP_AMOUNTS_USD,
  TOP_UP_MAX_USD,
  TOP_UP_MIN_USD,
  topUpAllowed,
  usdToMicroTokens,
  X64_RUNNER_SIZES,
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
test("the macOS label is compared with GitHub's macOS runner alone", () => {
  assert.deepEqual(
    MACOS_RUNNER_SIZES.map((size) => [size.label, size.vcpus, size.arch]),
    [["hive-macos", 4, "macos"]]
  )
  assert.equal(runnerSize("hive-macos").arch, "macos")
  assert.equal(competitorUsdPerMinuteForLabel("github", "hive-macos"), 0.062)
  // Blacksmith has no macOS runners, so such a job is left out, not guessed.
  assert.equal(competitorUsdPerMinuteForLabel("blacksmith", "hive-macos"), null)
  // "macos" is a kind of job, never what a machine reports itself as.
  assert.equal(runnerArch("macos"), null)
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
    X64_RUNNER_SIZES.map((size) => [size.label, size.vcpus, size.arch]),
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

test("a top-up is any whole number of dollars within the limits", () => {
  assert.equal(TOP_UP_MIN_USD, 1)
  for (const dollars of [TOP_UP_MIN_USD, 7, TOP_UP_MAX_USD])
    assert.equal(topUpAllowed(dollars), true, String(dollars))
  for (const dollars of TOP_UP_AMOUNTS_USD)
    assert.equal(topUpAllowed(dollars), true, String(dollars))
  for (const dollars of [
    0,
    -5,
    0.5,
    12.34,
    TOP_UP_MAX_USD + 1,
    "10",
    null,
    undefined,
    Number.NaN,
    Infinity,
  ])
    assert.equal(topUpAllowed(dollars), false, String(dollars))
})

test("a machine serves the kind its label names, and nothing else", () => {
  assert.equal(OWNER_CORES, 2)
  assert.equal(machineServes({ arch: "arm64" }, "arm64"), true)
  assert.equal(machineServes({ arch: "arm64" }, "x64"), false)
  assert.equal(machineServes({ arch: "x64" }, "x64"), true)
  assert.equal(machineServes({ arch: "x64" }, "arm64"), false)
  // A machine that does not say is arm64: all there was before there were two.
  assert.equal(machineServes({}, "arm64"), true)
  assert.equal(machineServes({ arch: null }, "arm64"), true)
  assert.equal(machineServes({}, "x64"), false)
  // macOS jobs need an Apple Silicon Mac whose owner opted in.
  assert.equal(machineServes({ arch: "arm64", macos: true }, "macos"), true)
  assert.equal(machineServes({ macos: true }, "macos"), true)
  assert.equal(machineServes({ arch: "arm64" }, "macos"), false)
  assert.equal(machineServes({ arch: "arm64", macos: false }, "macos"), false)
  assert.equal(machineServes({ arch: "x64", macos: true }, "macos"), false)
  // Opting in to macOS jobs does not stop a Mac taking Linux ones.
  assert.equal(machineServes({ arch: "arm64", macos: true }, "arm64"), true)
})
