// Runs daemon/release/install.sh for real against a release served from this
// process, in a home of its own, with the service manager stood in for.
// Restarting a daemon cancels the job it is in the middle of, so a run of
// the installer that finds nothing new must leave a running daemon alone,
// and one that has something new must have the daemon's jobs waited for
// before anything under it is replaced.
import assert from "node:assert/strict"
import { execFileSync, spawn } from "node:child_process"
import { createHash } from "node:crypto"
import {
  accessSync,
  chmodSync,
  constants,
  existsSync,
  mkdirSync,
  mkdtempSync,
  readFileSync,
  rmSync,
  writeFileSync,
} from "node:fs"
import { createServer } from "node:http"
import { arch, platform, tmpdir } from "node:os"
import { join, resolve } from "node:path"
import { once } from "node:events"
import { test } from "node:test"

const installer = resolve(import.meta.dirname, "../daemon/release/install.sh")
const mac = platform() === "darwin"
const linuxArch = arch() === "arm64" ? "arm64" : "amd64"

// The Linux half refuses a machine without KVM before it does anything
// else, and a job's own VM usually is one.
function unusable() {
  if (mac) return arch() === "arm64" ? "" : "the Mac half needs Apple Silicon"
  if (platform() !== "linux") return "the installer runs on macOS and Linux"
  try {
    accessSync("/dev/kvm", constants.R_OK | constants.W_OK)
    return ""
  } catch {
    return "this machine has no /dev/kvm its user can open"
  }
}

const sha256 = (file) =>
  createHash("sha256").update(readFileSync(file)).digest("hex")

function script(file, body) {
  writeFileSync(file, `#!/bin/sh\n${body}\n`)
  chmodSync(file, 0o755)
}

/**
 * A release in a directory: one daemon and one image, each of which a test
 * can replace with a newer one. The daemon stands in for hived only as far
 * as the installer asks it anything.
 */
function release(root, origin) {
  const dir = join(root, "release")
  const making = join(root, "making")
  mkdirSync(dir, { recursive: true })
  const state = { version: "9.9.0", daemon: "", image: 1 }
  const publish = () => {
    rmSync(making, { recursive: true, force: true })
    mkdirSync(join(making, "daemon"), { recursive: true })
    mkdirSync(join(making, "image"), { recursive: true })
    const imageVersion = `${state.image}-2.0.0`
    writeFileSync(
      join(making, "image/manifest.json"),
      JSON.stringify({ imageVersion: state.image, runnerVersion: "2.0.0" })
    )
    // It writes down what it is asked, and refuses to drain when told to:
    // a machine whose jobs outlast the wait.
    script(
      join(making, "daemon/hived"),
      `# ${state.daemon}
echo "hived $*" >> "${join(root, "asked.log")}"
# Signed out until a login that is let through; one is refused while the
# file "refuse" is there, as when the hive named is not one of the person's.
[ "$1" = status ] && [ -e "${join(root, "signed-out")}" ] && exit 1
if [ "$1" = login ]; then
  [ -t 0 ] && echo "login read a terminal" >> "${join(root, "asked.log")}"
  [ -n "\${HIVE_ENROLL_KEY:-}" ] && echo "login had key \${HIVE_ENROLL_KEY}" >> "${join(root, "asked.log")}"
  [ -e "${join(root, "refuse")}" ] && exit 1
  rm -f "${join(root, "signed-out")}"
fi
[ "$1" = drain ] && [ "\${2:-}" != --end ] && [ -e "${join(root, "busy")}" ] && exit 1
exit 0`
    )
    if (mac) {
      // The installer wants a signature macOS accepts. A script can carry
      // one made on the spot, and the archive keeps it.
      execFileSync("codesign", ["--sign", "-", join(making, "daemon/hived")])
      execFileSync("ditto", [
        "-c",
        "-k",
        join(making, "daemon"),
        join(dir, "hived.zip"),
      ])
      execFileSync("aa", [
        "archive",
        "-d",
        join(making, "image"),
        "-o",
        join(dir, `image-${state.image}.aar.aa`),
      ])
      writeFileSync(
        join(dir, "latest.json"),
        JSON.stringify({
          version: state.version,
          daemon: {
            url: `${origin}/hived.zip`,
            sha256: sha256(join(dir, "hived.zip")),
            teamId: "",
          },
          image: {
            version: imageVersion,
            url: `${origin}/image-${state.image}.aar`,
            parts: "aa",
            sha256: sha256(join(dir, `image-${state.image}.aar.aa`)),
            megabytes: 1,
          },
        })
      )
      return
    }
    execFileSync("tar", [
      "-czf",
      join(dir, "hived.tar.gz"),
      "-C",
      join(making, "daemon"),
      "hived",
    ])
    execFileSync("tar", [
      "-czf",
      join(dir, `image-${state.image}.tar.gz.aa`),
      "-C",
      join(making, "image"),
      "manifest.json",
    ])
    writeFileSync(
      join(dir, `linux-${linuxArch}.txt`),
      [
        `version=${state.version}`,
        `daemon_url=${origin}/hived.tar.gz`,
        `daemon_sha256=${sha256(join(dir, "hived.tar.gz"))}`,
        `image_version=${imageVersion}`,
        `image_url=${origin}/image-${state.image}.tar.gz`,
        "image_parts=aa",
        `image_sha256=${sha256(join(dir, `image-${state.image}.tar.gz.aa`))}`,
        "image_megabytes=1",
        "",
      ].join("\n")
    )
  }
  publish()
  return { dir, state, publish }
}

/** Mutate the published image fields without rebuilding the archives. */
function rewriteRelease(published, image) {
  if (mac) {
    const path = join(published.dir, "latest.json")
    const latest = JSON.parse(readFileSync(path, "utf8"))
    Object.assign(latest.image, image)
    writeFileSync(path, JSON.stringify(latest))
    return
  }
  const path = join(published.dir, `linux-${linuxArch}.txt`)
  let text = readFileSync(path, "utf8")
  if (image.sha256) {
    text = text.replace(
      /image_sha256=[a-f0-9]+/,
      `image_sha256=${image.sha256}`
    )
  }
  if (image.megabytes != null) {
    text = text.replace(
      /image_megabytes=\d+/,
      `image_megabytes=${image.megabytes}`
    )
  }
  writeFileSync(path, text)
}

/**
 * The service manager and, on Linux, the programs a VM is made of: each
 * writes down what it was asked. `running` is the file whose presence means
 * the daemon's service is up.
 */
function standIns(root) {
  const bin = join(root, "stand-ins")
  const asked = join(root, "asked.log")
  const running = join(root, "running")
  mkdirSync(bin)
  writeFileSync(asked, "")
  if (mac) {
    script(
      join(bin, "launchctl"),
      `echo "launchctl $*" >> "${asked}"
case "$1" in
  print) [ -e "${running}" ] ;;
  bootout) rm -f "${running}" ;;
  bootstrap) : > "${running}" ;;
esac`
    )
  } else {
    script(
      join(bin, "systemctl"),
      `echo "systemctl $*" >> "${asked}"
case "$*" in
  *is-active*) [ -e "${running}" ] ;;
  *restart*) : > "${running}" ;;
esac`
    )
    for (const name of ["journalctl", "loginctl", "qemu-img", "passt"])
      script(join(bin, name), "exit 0")
    for (const name of ["qemu-system-aarch64", "qemu-system-x86_64"])
      script(join(bin, name), "exit 0")
    script(join(bin, "virtiofsd"), 'echo "virtiofsd 1.13.2"')
  }
  // Record where curl writes, so a test can see the image was not staged
  // in /tmp. PATH is this directory first, so command -v curl is us.
  script(
    join(bin, "curl"),
    `echo "curl $*" >> "${asked}"
prev=""
for arg in "$@"; do
  if [ "$prev" = "-o" ]; then
    echo "curl-out $arg" >> "${asked}"
  fi
  prev=$arg
done
exec env PATH="/usr/bin:/bin" curl "$@"`
  )
  const lines = () => readFileSync(asked, "utf8").split("\n")
  return {
    bin,
    running,
    lines,
    // How many times the daemon was asked to stop taking jobs and wait.
    drains: () => lines().filter((line) => line === "hived drain").length,
    ends: () => lines().filter((line) => line === "hived drain --end").length,
    // How many times a running daemon was stopped or a stopped one started.
    restarts: () =>
      lines().filter((line) =>
        mac ? line.startsWith("launchctl bootstrap") : / restart /.test(line)
      ).length,
  }
}

/** A release served from this process, a home, and the stand-ins. */
async function setUp(t) {
  const root = mkdtempSync(join(tmpdir(), "hive-installer-"))
  t.after(() => rmSync(root, { recursive: true, force: true }))
  const home = join(root, "home")
  mkdirSync(home)

  let served
  const server = createServer((request, response) => {
    const file = join(served, new URL(request.url, "http://x").pathname)
    if (!existsSync(file)) return response.writeHead(404).end()
    response.end(readFileSync(file))
  })
  server.listen(0, "127.0.0.1")
  await once(server, "listening")
  t.after(() => server.close())
  const origin = `http://127.0.0.1:${server.address().port}`
  const published = release(root, origin)
  served = published.dir
  const service = standIns(root)

  const install = async (extra = {}, expected = 0, args = []) => {
    const child = spawn("sh", [installer, ...args], {
      env: {
        ...process.env,
        ...extra,
        HOME: home,
        XDG_CONFIG_HOME: "",
        HIVE_RELEASES: origin,
        HIVE_DEV: "1",
        PATH: `${service.bin}:${process.env.PATH}`,
      },
      stdio: ["ignore", "pipe", "pipe"],
      // A session of its own, with no terminal: as over ssh with a
      // command, or from an agent.
      detached: true,
    })
    let said = ""
    child.stdout.on("data", (chunk) => (said += chunk))
    child.stderr.on("data", (chunk) => (said += chunk))
    const [code] = await once(child, "exit")
    assert.equal(code, expected, said)
    return said
  }
  const asked = () => readFileSync(join(root, "asked.log"), "utf8")
  return { root, home, published, service, install, asked }
}

test(
  "the installer never restarts a daemon under a job it need not cancel",
  { skip: unusable() || false },
  async (t) => {
    const { root, home, published, service, install } = await setUp(t)

    // A machine that has never had it: the daemon is started.
    await install()
    assert.equal(service.restarts(), 1, "a first install starts the daemon")
    assert.ok(existsSync(service.running))
    assert.equal(service.drains(), 0, "there was no daemon to wait for")

    // Run again with the same release out: this is `hived update`, or
    // someone running the installer, on a machine that is in the middle
    // of a job. The daemon must not be touched.
    const again = await install()
    assert.equal(service.restarts(), 1, `nothing was new, yet:\n${again}`)
    assert.match(again, /already running, and was left as it is/)
    assert.equal(service.drains(), 0)

    // The same release, but the daemon is not running: it is started.
    rmSync(service.running)
    await install()
    assert.equal(service.restarts(), 2, "a stopped daemon is started")

    // A new daemon is what a restart is for, once the running one has been
    // asked to finish its jobs. While it has not, nothing is replaced.
    const installed = () =>
      readFileSync(join(home, ".local/bin/hived"), "utf8").split("\n")[1]
    published.state.version = "9.9.1"
    published.state.daemon = "newer"
    published.publish()
    writeFileSync(join(root, "busy"), "")
    const refused = await install({}, 1)
    assert.match(refused, /left running as it was/)
    assert.match(refused, /hived update --now/)
    assert.equal(service.restarts(), 2, "restarted under a job it was running")
    assert.equal(installed(), "# ", "the daemon on disk was replaced anyway")
    rmSync(join(root, "busy"))
    const drained = service.drains()
    await install()
    assert.equal(service.restarts(), 3, "a new daemon replaces the old one")
    assert.ok(service.drains() > drained, "it was not asked to finish first")
    assert.equal(installed(), "# newer")

    // So is a new image: the running daemon has the old one open.
    published.state.image = 2
    published.publish()
    const before = service.drains()
    await install()
    assert.equal(service.restarts(), 4, "a new image is taken up")
    assert.ok(service.drains() > before, "it was not asked to finish first")

    // HIVE_NOW=1 is for whoever means to cancel what is running.
    published.state.image = 3
    published.publish()
    const untilNow = service.drains()
    await install({ HIVE_NOW: "1" })
    assert.equal(service.restarts(), 5)
    assert.equal(service.drains(), untilNow, "asked to wait when told not to")

    // And after any of these, the next run is quiet again.
    await install()
    assert.equal(service.restarts(), 5)

    // The guest image is staged on the data disk, not in /tmp.
    const imageOuts = service
      .lines()
      .filter(
        (line) => line.startsWith("curl-out ") && /\/staging\//.test(line)
      )
    assert.ok(imageOuts.length > 0, "the image was not downloaded")
    const dataHome = mac
      ? join(home, "Library/Application Support/HiveWorker")
      : join(home, ".config/HiveWorker")
    for (const line of imageOuts) {
      const path = line.slice("curl-out ".length)
      assert.ok(
        path.startsWith(join(dataHome, "staging") + "/"),
        `image staged off the data disk:\n${path}`
      )
    }
  }
)

test(
  "a failed image update puts the machine back as it was",
  { skip: unusable() || false },
  async (t) => {
    const { home, published, service, install } = await setUp(t)
    await install()
    assert.equal(service.restarts(), 1)

    published.state.image = 2
    published.publish()
    rewriteRelease(published, { sha256: "deadbeef" })

    const untilFail = service.drains()
    const failed = await install({}, 1)
    assert.match(failed, /does not match its published checksum/)
    assert.match(failed, /put back as it was/)
    assert.ok(service.drains() > untilFail, "it was not asked to finish first")
    assert.ok(service.ends() > 0, "the pause the drain put on was not lifted")
    assert.equal(service.restarts(), 1, "restarted after a failed image fetch")
    const dataHome = mac
      ? join(home, "Library/Application Support/HiveWorker")
      : join(home, ".config/HiveWorker")
    assert.ok(
      !existsSync(join(dataHome, "staging")),
      "the staging directory was left behind"
    )
  }
)

test(
  "the installer refuses an image that will not fit, before it pauses the machine",
  { skip: unusable() || false },
  async (t) => {
    const { home, published, service, install } = await setUp(t)
    await install()
    published.state.image = 2
    published.publish()
    // As large as the disk is free, so it never fits whatever disk this
    // runs on: the installer wants three times the image.
    const freeKb = execFileSync("df", ["-Pk", home], { encoding: "utf8" })
      .split("\n")[1]
      .trim()
      .split(/\s+/)[3]
    rewriteRelease(published, { megabytes: Math.ceil(Number(freeKb) / 1024) })
    const untilSpace = service.drains()
    const refused = await install({}, 1)
    assert.match(refused, /not enough room/)
    assert.equal(
      service.drains(),
      untilSpace,
      "paused before it knew it would fail"
    )
    assert.equal(service.ends(), 0)
    assert.equal(service.restarts(), 1)
  }
)

test(
  "the installer finishes with nobody at a terminal, and says what is left",
  { skip: unusable() || false },
  async (t) => {
    const { root, home, service, install, asked } = await setUp(t)
    writeFileSync(join(root, "signed-out"), "")
    writeFileSync(join(root, "refuse"), "")

    // Not added to a hive: everything else is still installed, the service
    // is set up but not started, and the one command left is said.
    const refused = await install({}, 1, ["--hive", "acme"])
    assert.match(refused, /not in a hive yet/)
    assert.match(refused, /hived login --hive acme/)
    assert.ok(asked().includes("hived login --hive acme\n"), asked())
    assert.ok(!asked().includes("login read a terminal"), "it asked a terminal")
    assert.equal(service.restarts(), 0, "a daemon in no hive was started")
    assert.ok(!existsSync(service.running))
    assert.ok(existsSync(join(home, ".local/bin/hived")))
    assert.ok(
      existsSync(
        mac
          ? join(
              home,
              "Library/LaunchAgents/com.thecomputerplumbers.hiveworker.plist"
            )
          : join(home, ".config/systemd/user/hiveworker.service")
      ),
      "the service was not set up"
    )

    // HIVE says the same, and a login let through starts the daemon.
    rmSync(join(root, "refuse"))
    const added = await install({ HIVE: "globex" })
    assert.ok(asked().includes("hived login --hive globex\n"), asked())
    assert.equal(service.restarts(), 1, added)
    assert.ok(existsSync(service.running))

    // An enroll key reaches hived in its environment, never on its command
    // line, and is not repeated in what the installer says.
    writeFileSync(join(root, "signed-out"), "")
    writeFileSync(join(root, "refuse"), "")
    const keyed = await install({}, 1, [
      "--enroll-key",
      "hwe_secret",
      "--hive",
      "acme",
    ])
    assert.ok(asked().includes("login had key hwe_secret\n"), asked())
    assert.ok(
      !asked().includes("--enroll-key"),
      "the key was on hived's command line"
    )
    assert.ok(!keyed.includes("hwe_secret"), keyed)
    assert.match(keyed, /HIVE_ENROLL_KEY=<the key> .*hived login --hive acme/)

    // Anything else on the command line is refused before anything is done.
    assert.match(await install({}, 2, ["--hives", "acme"]), /--hive <name>/)
  }
)
