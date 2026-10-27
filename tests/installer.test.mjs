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
    // logind, asked as the service asks, refuses a lock against sleep until
    // the file "awake-allowed" is there; sudo asks for a password until
    // "sudo-free" is, and then writes the rule to "polkit-rule" instead.
    script(join(bin, "systemd-inhibit"), "exit 0")
    script(
      join(bin, "systemd-run"),
      `echo "systemd-run $*" >> "${asked}"
[ -e "${join(root, "awake-allowed")}" ]`
    )
    script(
      join(bin, "sudo"),
      `echo "sudo $*" >> "${asked}"
[ -e "${join(root, "sudo-free")}" ] && [ "$1" = -n ] || exit 1
case "$2" in
  tee) cat > "${join(root, "polkit-rule")}" ;;
  chmod) ;;
  *) exit 1 ;;
esac`
    )
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
  // How many more requests for a path are cut off halfway, as a stream a
  // busy server resets is; and every path asked for from somewhere in it.
  const cuts = new Map()
  const resumed = []
  const server = createServer((request, response) => {
    const path = new URL(request.url, "http://x").pathname
    const file = join(served, path)
    if (!existsSync(file)) return response.writeHead(404).end()
    let body = readFileSync(file)
    const range = /^bytes=(\d+)-$/.exec(request.headers.range ?? "")
    if (range && Number(range[1]) > 0) {
      resumed.push(path)
      const from = Number(range[1])
      if (from >= body.length) return response.writeHead(416).end()
      response.writeHead(206, {
        "Content-Range": `bytes ${from}-${body.length - 1}/${body.length}`,
        "Content-Length": body.length - from,
      })
      body = body.subarray(from)
    } else response.writeHead(200, { "Content-Length": body.length })
    if ((cuts.get(path) ?? 0) > 0) {
      cuts.set(path, cuts.get(path) - 1)
      response.write(body.subarray(0, Math.ceil(body.length / 2)))
      return setTimeout(() => response.socket.destroy(), 50)
    }
    response.end(body)
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
        HIVE_RETRY_SECONDS: "0",
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
  return { root, home, published, service, install, asked, cuts, resumed }
}

const imagePiece = (image) =>
  mac ? `/image-${image}.aar.aa` : `/image-${image}.tar.gz.aa`

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

test(
  "a cut-off update replaces nothing, and the next run carries on from where it stopped",
  { skip: unusable() || false },
  async (t) => {
    const { home, published, service, install, cuts, resumed } = await setUp(t)
    await install()
    const dataHome = mac
      ? join(home, "Library/Application Support/HiveWorker")
      : join(home, ".config/HiveWorker")
    const daemon = () =>
      readFileSync(join(home, ".local/bin/hived"), "utf8").split("\n")[1]
    const image = () =>
      JSON.parse(readFileSync(join(dataHome, "image/manifest.json"), "utf8"))
        .imageVersion

    // A new daemon and a new image, and an image that is cut off every
    // time it is asked for: the installer gives up, having replaced neither.
    published.state.version = "9.9.1"
    published.state.daemon = "newer"
    published.state.image = 2
    published.publish()
    cuts.set(imagePiece(2), 100)
    const failed = await install({}, 1)
    assert.match(failed, /kept being cut off/)
    assert.match(failed, /carries on from there/)
    assert.match(failed, /put back as it was/)
    assert.equal(daemon(), "# ", "the new daemon went in without its image")
    assert.equal(image(), 1, "the old image was replaced")
    assert.equal(service.restarts(), 1)
    assert.ok(resumed.includes(imagePiece(2)), "no try carried on from another")

    // The link is better now, though the first try is still cut off: the
    // piece is finished from what is on disk, and everything goes in.
    cuts.set(imagePiece(2), 1)
    const before = resumed.length
    await install()
    assert.ok(resumed.length > before, "the next run started over")
    assert.equal(daemon(), "# newer")
    assert.equal(image(), 2)
    assert.equal(service.restarts(), 2)
    assert.ok(
      !existsSync(join(dataHome, "staging")),
      "the staging directory was left behind"
    )
  }
)

test(
  "the installer lets hived hold off sleep, using sudo only if it asks for no password",
  {
    skip:
      unusable() ||
      (mac && "caffeinate needs no rule") ||
      (!existsSync("/etc/polkit-1/rules.d") && "this machine has no polkit"),
  },
  async (t) => {
    const { root, service, install } = await setUp(t)
    const sudo = () =>
      service.lines().filter((line) => line.startsWith("sudo "))

    // sudo would ask for a password: the command is printed instead.
    const printed = await install()
    assert.match(
      printed,
      /\| sudo tee \/etc\/polkit-1\/rules\.d\/49-hiverunner-[a-z0-9_-]+\.rules >\/dev\/null && sudo chmod 0644 /
    )
    assert.ok(sudo().length > 0, "sudo was not tried")
    for (const line of sudo())
      assert.ok(
        line.startsWith("sudo -n "),
        `sudo may ask for a password: ${line}`
      )

    // sudo asks for none: the rule is written.
    writeFileSync(join(root, "sudo-free"), "")
    const written = await install()
    assert.match(
      written,
      /Let hived keep this machine from sleeping while it runs a job/
    )
    assert.match(
      readFileSync(join(root, "polkit-rule"), "utf8"),
      /action\.id == "org\.freedesktop\.login1\.inhibit-block-sleep" && subject\.user == "[a-z0-9_-]+"/
    )

    // Already allowed: nothing is said, and sudo is not asked again.
    writeFileSync(join(root, "awake-allowed"), "")
    const asked = sudo().length
    const quiet = await install()
    assert.equal(sudo().length, asked, "sudo was asked with the lock allowed")
    assert.doesNotMatch(quiet, /polkit|sleeping while it runs/)
  }
)
