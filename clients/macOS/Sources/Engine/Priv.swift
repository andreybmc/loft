// Swift wrapper around setuid loft-priv: install helper, hold utun, split default, restore.

import Foundation
import AppKit
import Darwin

/// Swift wrapper around setuid loft-priv: install helper, hold utun, split default, restore.
enum Priv {
    private static var heldBy: [String: Process] = [:]
    private static var tunNameBy: [String: String] = [:]
    private static let heldLock = NSLock()
    private(set) static var tunName = ""
    private static var origGW = ""

    /// Runs `cmd` as root: already euid 0, or osascript on the main thread.
    static func runAdmin(_ cmd: String) throws {
        if geteuid() == 0 {
            try run("/bin/sh", ["-c", cmd])
            return
        }
        if pthread_main_np() != 0 {
            try runAdminNow(cmd)
            return
        }
        var caught: Error?
        let sem = DispatchSemaphore(value: 0)
        DispatchQueue.main.async {
            NSApp.activate(ignoringOtherApps: true)
            do { try runAdminNow(cmd) } catch { caught = error }
            sem.signal()
        }
        sem.wait()
        if let e = caught { throw e }
    }

    /// osascript `do shell script … with administrator privileges`.
    private static func runAdminNow(_ cmd: String) throws {
        let quoted = cmd
            .replacingOccurrences(of: "\\", with: "\\\\")
            .replacingOccurrences(of: "\"", with: "\\\"")
        try run("/usr/bin/osascript", [
            "-e",
            "do shell script \"\(quoted)\" with prompt \"loft — маршрут машины\" with administrator privileges",
        ])
    }

    /// Runs `path` with `args` and logs stdout/stderr; throws on non-zero exit.
    static func run(_ path: String, _ args: [String]) throws {
        let p = Process()
        p.launchPath = path
        p.arguments = args
        let errp = Pipe()
        let outp = Pipe()
        p.standardError = errp
        p.standardOutput = outp
        p.launch()
        p.waitUntilExit()
        let err = String(data: errp.fileHandleForReading.readDataToEndOfFile(), encoding: .utf8) ?? ""
        let out = String(data: outp.fileHandleForReading.readDataToEndOfFile(), encoding: .utf8) ?? ""
        for raw in (err + "\n" + out).components(separatedBy: "\n") {
            let line = raw.trimmingCharacters(in: .whitespacesAndNewlines)
            if !line.isEmpty { LoftLog.shared.add("tun", line) }
        }
        if p.terminationStatus != 0 {
            throw NSError(domain: "loft", code: 30, userInfo: [NSLocalizedDescriptionKey: err.isEmpty ? "priv \(p.terminationStatus)" : err])
        }
    }

    /// Path to bundled loft-priv next to the app executable.
    static func bundledPriv() -> String? {
        let bundle = Bundle.main.bundlePath
        let a = (bundle as NSString).appendingPathComponent("Contents/MacOS/loft-priv")
        if FileManager.default.isExecutableFile(atPath: a) { return a }
        if let exe = Bundle.main.executablePath {
            let b = (exe as NSString).deletingLastPathComponent + "/loft-priv"
            if FileManager.default.isExecutableFile(atPath: b) { return b }
        }
        return nil
    }

    /// Installed setuid copy under Application Support/loft.
    static func installedPriv() -> String {
        let dir = NSHomeDirectory() + "/Library/Application Support/loft"
        return dir + "/loft-priv"
    }

    /// True if `path` is owned by root and has the setuid bit.
    static func isSetuidRoot(_ path: String) -> Bool {
        var st = stat()
        guard stat(path, &st) == 0 else { return false }
        return st.st_uid == 0 && (st.st_mode & S_ISUID) != 0
    }

    /// Copies bundled loft-priv to Application Support and chmod 4755 (one admin prompt).
    static func ensureSetuid() throws {
        guard let bundled = bundledPriv() else {
            throw NSError(domain: "loft", code: 40, userInfo: [NSLocalizedDescriptionKey: "loft-priv"])
        }
        let dest = installedPriv()
        if isSetuidRoot(dest), sameFile(bundled, dest) { return }
        LoftLog.shared.add("loft", "tun: one admin to install loft-priv")
        let dir = (dest as NSString).deletingLastPathComponent
        let dq = quote(dest)
        let bq = quote(bundled)
        let dirq = quote(dir)
        try runAdmin("mkdir -p \(dirq) && cp \(bq) \(dq) && chown root:wheel \(dq) && chmod 4755 \(dq)")
        if !isSetuidRoot(dest) {
            throw NSError(domain: "loft", code: 40, userInfo: [NSLocalizedDescriptionKey: "loft-priv setuid"])
        }
    }

    /// Spawns a held loft-priv: utun, overlay routes, optional 0/1 default.
    /// `key` is the profile id so several holders can stay up together.
    static func upTUN(sock: String, ip: String, url: String, defaultRoute: Bool, dns: String = "", mtu: Int = 0, key: String = "default") throws {
        try ensureSetuid()
        let peer = peer4(ip)
        let gw = gateway()
        var useMTU = mtu
        if useMTU < 576 { useMTU = 1280 }
        if useMTU > 1500 { useMTU = 1500 }
        var args = ["--sock", sock, "--ip", ip, "--peer", peer, "--mtu", "\(useMTU)"]
        if defaultRoute { args.append("--default") }
        if !gw.isEmpty { args += ["--gw", gw] }
        if defaultRoute {
            let excl = excludeHosts(url: url, overlayIP: ip)
            if !excl.isEmpty { args += ["--exclude", excl.joined(separator: ",")] }
        }
        // Overlay DNS is 10.8.0.1. Pushing it onto every Network Service
        // kills name lookup (ping by IP still works). Keep the machine resolvers.
        _ = dns
        LoftLog.shared.add("loft", "tun up key=\(key) ip=\(ip) peer=\(peer) mtu=\(useMTU) default=\(defaultRoute ? 1 : 0) gw=\(gw)")
        stopHeld(key: key)
        if !isRealGW(origGW) {
            origGW = gw
            rememberGW(gw)
        }
        rememberIP(ip)
        LoftLog.shared.snapshot("up-before")
        try spawnHeld(installedPriv(), args, key: key)
        logRoute("1.1.1.1")
        LoftLog.shared.snapshot("up-after")
    }

    /// utun name for a live holder, or empty.
    static func tunName(for key: String) -> String {
        heldLock.lock(); defer { heldLock.unlock() }
        return tunNameBy[key] ?? ""
    }

    /// True when at least one loft-priv holder is still running.
    static func hasHolders() -> Bool {
        heldLock.lock(); defer { heldLock.unlock() }
        return heldBy.contains { _, p in p.isRunning }
    }

    /// One-shot --set-default / --clear-default on the live utun.
    static func setDefaultRoute(_ on: Bool, url: String, overlayIP: String = "", ifname: String = "") {
        guard isSetuidRoot(installedPriv()) else { return }
        if on, !isRealGW(origGW) {
            let now = gateway()
            if isRealGW(now) {
                origGW = now
                rememberGW(now)
            }
        }
        let gw = savedGW()
        let ip = overlayIP.isEmpty ? rememberedIP() : overlayIP
        let excl = excludeHosts(url: url, overlayIP: ip)
        LoftLog.shared.snapshot(on ? "default-on-before" : "default-off-before")
        if on {
            let name = ifname.isEmpty ? tunName : ifname
            if name.isEmpty {
                LoftLog.shared.add("loft", "tun default: no if")
                return
            }
            var args = ["--set-default", "--name", name]
            if !gw.isEmpty { args += ["--gw", gw] }
            if !excl.isEmpty { args += ["--exclude", excl.joined(separator: ",")] }
            LoftLog.shared.add("loft", "tun default on if=\(name) gw=\(gw)")
            try? run(installedPriv(), args)
        } else {
            var args = ["--clear-default"]
            if !gw.isEmpty { args += ["--gw", gw] }
            if !excl.isEmpty { args += ["--exclude", excl.joined(separator: ",")] }
            LoftLog.shared.add("loft", "tun default off gw=\(gw)")
            try? run(installedPriv(), args)
        }
        logRoute("1.1.1.1")
        LoftLog.shared.snapshot(on ? "default-on-after" : "default-off-after")
    }

    /// Launches loft-priv and waits for `hold utunN` on stderr.
    private static func spawnHeld(_ path: String, _ args: [String], key: String) throws {
        let p = Process()
        p.launchPath = path
        p.arguments = args
        let errp = Pipe()
        p.standardError = errp
        p.standardOutput = Pipe()
        let lock = NSLock()
        var blob = ""
        var ok = false
        let sem = DispatchSemaphore(value: 0)
        errp.fileHandleForReading.readabilityHandler = { h in
            let data = h.availableData
            if data.isEmpty { return }
            let s = String(data: data, encoding: .utf8) ?? String(data: data, encoding: .isoLatin1) ?? ""
            fputs(s, stderr)
            lock.lock(); blob += s; lock.unlock()
            for raw in s.components(separatedBy: "\n") {
                let line = raw.trimmingCharacters(in: .whitespacesAndNewlines)
                if !line.isEmpty { LoftLog.shared.add("tun", line) }
                if line.contains("hold ") {
                    let parts = line.split(separator: " ")
                    if parts.count >= 3 {
                        let name = String(parts[2])
                        heldLock.lock()
                        tunNameBy[key] = name
                        tunName = name
                        heldLock.unlock()
                    }
                    lock.lock(); ok = true; lock.unlock()
                    sem.signal()
                }
            }
        }
        p.terminationHandler = { _ in
            errp.fileHandleForReading.readabilityHandler = nil
            sem.signal()
        }
        p.launch()
        heldLock.lock(); heldBy[key] = p; heldLock.unlock()
        _ = sem.wait(timeout: .now() + 8)
        lock.lock(); let ready = ok; lock.unlock()
        if !ready {
            stopHeld(key: key)
            throw NSError(domain: "loft", code: 30, userInfo: [NSLocalizedDescriptionKey: "tun hold"])
        }
    }

    /// SIGTERM (then --force) one held loft-priv, or every holder when `key` is nil.
    /// GUI cannot signal ruid 0 itself. `--reap` only when dropping all holders.
    static func stopHeld(key: String? = nil) {
        heldLock.lock()
        let keys: [String]
        if let key = key {
            keys = heldBy[key] != nil ? [key] : []
        } else {
            keys = Array(heldBy.keys)
        }
        var procs: [Process] = []
        for k in keys {
            if let p = heldBy.removeValue(forKey: k) { procs.append(p) }
            tunNameBy.removeValue(forKey: k)
        }
        tunName = tunNameBy.values.first ?? ""
        let reapAll = key == nil
        heldLock.unlock()
        for p in procs where p.isRunning {
            let pid = p.processIdentifier
            if isSetuidRoot(installedPriv()) {
                try? run(installedPriv(), ["--kill", "\(pid)"])
            } else {
                p.terminate()
            }
            let deadline = Date().addingTimeInterval(2)
            while p.isRunning && Date() < deadline {
                Thread.sleep(forTimeInterval: 0.05)
            }
            if p.isRunning, isSetuidRoot(installedPriv()) {
                try? run(installedPriv(), ["--kill", "\(pid)", "--force"])
            }
        }
        if reapAll, isSetuidRoot(installedPriv()) {
            try? run(installedPriv(), ["--reap"])
        }
    }

    /// Boot-time cleanup: restore routes and reap leftover holders.
    static func reapStale() {
        restoreRoutes()
    }

    /// Path of the remembered overlay IP file.
    private static func tunIPFile() -> String {
        NSHomeDirectory() + "/Library/Application Support/loft/tun-ip"
    }

    /// Persists overlay IP for a later --down.
    private static func rememberIP(_ ip: String) {
        try? ip.write(toFile: tunIPFile(), atomically: true, encoding: .utf8)
    }

    /// Overlay IP from the last upTUN, or empty.
    private static func rememberedIP() -> String {
        guard let s = try? String(contentsOfFile: tunIPFile(), encoding: .utf8) else { return "" }
        return s.trimmingCharacters(in: .whitespacesAndNewlines)
    }

    /// Deletes the remembered overlay IP file.
    private static func forgetIP() {
        try? FileManager.default.removeItem(atPath: tunIPFile())
    }

    /// Path of the remembered pre-loft gateway file.
    private static func gwFile() -> String {
        NSHomeDirectory() + "/Library/Application Support/loft/orig-gw"
    }

    /// Persists a real (non-utun) gateway.
    private static func rememberGW(_ gw: String) {
        guard isRealGW(gw) else { return }
        try? gw.write(toFile: gwFile(), atomically: true, encoding: .utf8)
    }

    /// Gateway from disk, or empty.
    private static func rememberedGW() -> String {
        guard let s = try? String(contentsOfFile: gwFile(), encoding: .utf8) else { return "" }
        return s.trimmingCharacters(in: .whitespacesAndNewlines)
    }

    /// Memory, then disk, then live `route get default`.
    static func savedGW() -> String {
        if isRealGW(origGW) { return origGW }
        let disk = rememberedGW()
        if isRealGW(disk) { return disk }
        return gateway()
    }

    /// False for empty, link-local dummy, link#, or utun.
    private static func isRealGW(_ gw: String) -> Bool {
        if gw.isEmpty { return false }
        if gw == "169.254.8.1" { return false }
        if gw.hasPrefix("link#") { return false }
        if gw.hasPrefix("utun") { return false }
        return true
    }

    /// Logs `route get dest` gateway and iface.
    static func logRoute(_ dest: String) {
        let p = activePath(dest)
        LoftLog.shared.add("loft", "route \(dest) gw=\(p.gw) if=\(p.iface)")
    }

    /// Parses `route -n get dest` for gateway and interface.
    static func activePath(_ dest: String) -> (gw: String, iface: String) {
        let lines = shellLines("/sbin/route", ["-n", "get", dest])
        var iface = "", gw = ""
        for raw in lines {
            let line = raw.trimmingCharacters(in: .whitespaces)
            if line.hasPrefix("interface:") {
                iface = line.replacingOccurrences(of: "interface:", with: "").trimmingCharacters(in: .whitespaces)
            }
            if line.hasPrefix("gateway:") {
                gw = line.replacingOccurrences(of: "gateway:", with: "").trimmingCharacters(in: .whitespaces)
            }
        }
        return (gw, iface)
    }

    /// 1.1.1.1 and 0/0 leave via a real (non-utun) gateway.
    static func internetRestored() -> Bool {
        let p = activePath("1.1.1.1")
        if p.iface.hasPrefix("utun") || p.gw.isEmpty || !isRealGW(p.gw) { return false }
        let d = activePath("default")
        if d.iface.hasPrefix("utun") || d.gw.isEmpty || !isRealGW(d.gw) { return false }
        return true
    }

    /// Routes, DNS, loft-priv, orig-gw. Used by the extended journal.
    static func diagLines() -> [String] {
        var out: [String] = []
        /// Appends a trimmed diag line.
        func add(_ s: String) {
            let t = s.trimmingCharacters(in: .whitespacesAndNewlines)
            if !t.isEmpty { out.append(t) }
        }
        add("orig mem=\(origGW) disk=\(rememberedGW()) saved=\(savedGW()) tun=\(tunName) ip=\(rememberedIP())")
        let d = activePath("default")
        add("default gw=\(d.gw) if=\(d.iface)")
        let p = activePath("1.1.1.1")
        add("1.1.1.1 gw=\(p.gw) if=\(p.iface)")
        for line in shellLines("/usr/sbin/netstat", ["-rn", "-f", "inet"]) {
            let t = line.trimmingCharacters(in: .whitespaces)
            if t.hasPrefix("default") || t.contains("utun") || t.contains("10.8.") || t.contains("169.254.8") {
                add("rt \(t)")
            }
        }
        let dns = dnsIPs()
        add("dns \(dns.isEmpty ? "-" : dns.joined(separator: ","))")
        for line in shellLines("/sbin/ifconfig", ["-l"]) {
            add("if \(line)")
        }
        for line in shellLines("/bin/ps", ["-ax", "-o", "pid=,ucomm="]) {
            let t = line.trimmingCharacters(in: .whitespaces)
            if t.hasSuffix("loft-priv") || t.hasSuffix("loft-front") {
                add("ps \(t)")
            }
        }
        if out.count > 80 { return Array(out.prefix(80)) }
        return out
    }

    /// Runs `path` and splits stdout into lines (read before wait to avoid deadlock).
    private static func shellLines(_ path: String, _ args: [String]) -> [String] {
        let p = Process()
        p.launchPath = path
        p.arguments = args
        let outp = Pipe()
        let errp = Pipe()
        p.standardOutput = outp
        p.standardError = errp
        p.standardInput = FileHandle.nullDevice
        p.launch()
        // Read before waitUntilExit: a full `ps` fills the pipe and deadlocks.
        let data = outp.fileHandleForReading.readDataToEndOfFile()
        _ = errp.fileHandleForReading.readDataToEndOfFile()
        p.waitUntilExit()
        let s = String(data: data, encoding: .utf8) ?? ""
        return s.components(separatedBy: "\n")
    }

    /// Alias for restoreRoutes (drop hold / split default).
    static func resetHold() {
        restoreRoutes()
    }

    /// Drop split default / blackhole and put 0/0 back on the pre-loft gateway.
    /// Also SIGTERM leftover loft-priv holders (they run as root).
    static func restoreRoutes() {
        downTUN(url: "", hold: false, ip: rememberedIP(), restoreDefault: defaultStamp())
    }

    /// True if loft-priv stamped that it installed the split default.
    private static func defaultStamp() -> Bool {
        FileManager.default.fileExists(atPath: "/tmp/loft-did-default")
    }

    /// Stops the holder and --down/--hold/--restore. No admin prompt on quit.
    /// `othersRemain` keeps sibling utuns: overlay-only cleanup, no `--reap` / `--restore`.
    static func downTUN(url: String, hold: Bool, ip: String = "", restoreDefault: Bool? = nil, key: String = "default", othersRemain: Bool = false) {
        let useIP = ip.isEmpty ? rememberedIP() : ip
        var gw = savedGW()
        let takeDefault = (restoreDefault ?? defaultStamp()) && !othersRemain
        LoftLog.shared.snapshot("down-before")
        // No ensureSetuid here: quit must not block on an admin dialog.
        // Kill utun first. Rewrite 0/0 only if loft actually installed it.
        if othersRemain {
            stopHeld(key: key)
        } else {
            stopHeld(key: key)
            if !hasHolders() { stopHeld(key: nil) }
        }
        if !isRealGW(gw) { gw = savedGW() }
        if isSetuidRoot(installedPriv()) {
            let host = frontHost(url)
            let ips = resolve(host)
            var args: [String]
            if hold {
                args = ["--down", "--hold"]
            } else if takeDefault {
                args = ["--restore"]
            } else {
                args = ["--down", "--overlay"]
            }
            if !useIP.isEmpty {
                args += ["--ip", useIP, "--peer", peer4(useIP)]
            }
            if !gw.isEmpty { args += ["--gw", gw] }
            if !ips.isEmpty { args += ["--exclude", ips.joined(separator: ",")] }
            LoftLog.shared.add("loft", "tun down hold=\(hold ? 1 : 0) default=\(takeDefault ? 1 : 0) ip=\(useIP) gw=\(gw)")
            try? run(installedPriv(), args)
            if !hold && takeDefault {
                for attempt in 0..<4 {
                    if internetRestored() { break }
                    Thread.sleep(forTimeInterval: 0.15)
                    var retry = ["--restore"]
                    if !gw.isEmpty { retry += ["--gw", gw] }
                    LoftLog.shared.add("loft", "tun restore retry \(attempt + 1) gw=\(gw)")
                    try? run(installedPriv(), retry)
                }
            }
        }
        if !hold && !othersRemain {
            if !takeDefault || internetRestored() {
                origGW = ""
                forgetIP()
            } else {
                LoftLog.shared.add("loft", "tun restore incomplete gw=\(gw)")
            }
        }
        logRoute("1.1.1.1")
        logRoute("default")
        LoftLog.shared.snapshot("down-after")
    }

    /// Front host and DNS IPs to pin on the pre-loft gateway.
    static func excludeIPs(url: String) -> [String] {
        excludeHosts(url: url, overlayIP: rememberedIP())
    }

    /// Front IPs plus machine DNS, skipping the overlay /24.
    static func excludeHosts(url: String, overlayIP: String) -> [String] {
        var seen = Set<String>()
        var out: [String] = []
        /// Dedupes `raw` unless it shares /24 with the overlay.
        func add(_ raw: String) {
            let t = raw.trimmingCharacters(in: .whitespacesAndNewlines)
            if t.isEmpty { return }
            if sameSlash24(t, overlayIP) { return }
            if seen.insert(t).inserted { out.append(t) }
        }
        let host = frontHost(url)
        let resolved = resolve(host)
        if resolved.isEmpty { add(host) } else { resolved.forEach(add) }
        dnsIPs().forEach(add)
        return out
    }

    /// True if `a` and `b` share the first three IPv4 octets.
    static func sameSlash24(_ a: String, _ b: String) -> Bool {
        let pa = a.split(separator: ".")
        let pb = b.split(separator: ".")
        guard pa.count == 4, pb.count == 4 else { return false }
        return pa[0] == pb[0] && pa[1] == pb[1] && pa[2] == pb[2]
    }

    /// IPv4 nameservers from `scutil --dns`.
    static func dnsIPs() -> [String] {
        let p = Process()
        p.launchPath = "/usr/sbin/scutil"
        p.arguments = ["--dns"]
        let out = Pipe()
        p.standardOutput = out
        p.standardError = Pipe()
        p.launch()
        p.waitUntilExit()
        let s = String(data: out.fileHandleForReading.readDataToEndOfFile(), encoding: .utf8) ?? ""
        var ips: [String] = []
        var seen = Set<String>()
        for raw in s.components(separatedBy: .newlines) {
            let line = raw.trimmingCharacters(in: .whitespaces)
            guard let r = line.range(of: "nameserver[") else { continue }
            let rest = line[r.upperBound...]
            guard let colon = rest.firstIndex(of: ":") else { continue }
            let ip = rest[rest.index(after: colon)...].trimmingCharacters(in: .whitespaces)
            if ip.contains(":"), !ip.contains(".") { continue }
            if seen.insert(ip).inserted { ips.append(ip) }
        }
        return ips
    }

    /// SOCKS CONNECT 1.1.1.1:80 and read an HTTP reply. ICMP can work while TCP does not.
    static func loftCarriesTCP(_ socks: String) -> Bool {
        let parts = socks.trimmingCharacters(in: .whitespaces).split(separator: ":")
        guard parts.count == 2, let sport = UInt16(parts[1]) else { return false }
        var addr = sockaddr_in()
        addr.sin_len = UInt8(MemoryLayout<sockaddr_in>.size)
        addr.sin_family = sa_family_t(AF_INET)
        addr.sin_port = sport.bigEndian
        let host = String(parts[0])
        _ = host.withCString { inet_pton(AF_INET, $0, &addr.sin_addr) }
        let fd = socket(AF_INET, SOCK_STREAM, IPPROTO_TCP)
        if fd < 0 { return false }
        defer { close(fd) }
        var tv = timeval(tv_sec: 6, tv_usec: 0)
        setsockopt(fd, SOL_SOCKET, SO_RCVTIMEO, &tv, socklen_t(MemoryLayout<timeval>.size))
        setsockopt(fd, SOL_SOCKET, SO_SNDTIMEO, &tv, socklen_t(MemoryLayout<timeval>.size))
        let cr = withUnsafePointer(to: &addr) {
            $0.withMemoryRebound(to: sockaddr.self, capacity: 1) { connect(fd, $0, socklen_t(MemoryLayout<sockaddr_in>.size)) }
        }
        if cr != 0 { return false }
        /// Sends all of `b`, or false on short write.
        func sendAll(_ b: [UInt8]) -> Bool {
            var off = 0
            while off < b.count {
                let n = b.withUnsafeBytes { raw -> Int in
                    send(fd, raw.baseAddress!.advanced(by: off), b.count - off, 0)
                }
                if n <= 0 { return false }
                off += n
            }
            return true
        }
        /// Receives exactly `n` bytes, or nil on EOF/timeout.
        func recvN(_ n: Int) -> [UInt8]? {
            var buf = [UInt8](repeating: 0, count: n)
            var off = 0
            while off < n {
                let k = buf.withUnsafeMutableBytes { raw -> Int in
                    recv(fd, raw.baseAddress!.advanced(by: off), n - off, 0)
                }
                if k <= 0 { return nil }
                off += k
            }
            return buf
        }
        if !sendAll([0x05, 0x01, 0x00]) { return false }
        guard let hello = recvN(2), hello == [0x05, 0x00] else { return false }
        if !sendAll([0x05, 0x01, 0x00, 0x01, 1, 1, 1, 1, 0x00, 0x50]) { return false }
        guard let rep = recvN(10), rep.count >= 2, rep[1] == 0 else { return false }
        let req = Array("GET / HTTP/1.0\r\nHost: 1.1.1.1\r\n\r\n".utf8)
        if !sendAll(req) { return false }
        var buf = [UInt8](repeating: 0, count: 16)
        let n = buf.withUnsafeMutableBytes { raw -> Int in
            recv(fd, raw.baseAddress, raw.count, 0)
        }
        if n < 4 { return false }
        return buf[0] == 0x48 && buf[1] == 0x54 && buf[2] == 0x54 && buf[3] == 0x50
    }

    /// True if both paths exist and have identical contents.
    private static func sameFile(_ a: String, _ b: String) -> Bool {
        guard let da = try? Data(contentsOf: URL(fileURLWithPath: a)),
              let db = try? Data(contentsOf: URL(fileURLWithPath: b)) else { return false }
        return da == db
    }

    /// Single-quotes `s` for /bin/sh.
    private static func quote(_ s: String) -> String {
        "'" + s.replacingOccurrences(of: "'", with: "'\\''") + "'"
    }

    /// Host of a loft URL, stripping scheme, path, and port.
    static func frontHost(_ url: String) -> String {
        var s = url.trimmingCharacters(in: .whitespacesAndNewlines)
        let low = s.lowercased()
        for p in ["wss://", "ws://", "https://", "http://"] {
            if low.hasPrefix(p) {
                s = String(s.dropFirst(p.count))
                break
            }
        }
        if let i = s.firstIndex(where: { $0 == "/" || $0 == "?" }) {
            s = String(s[..<i])
        }
        if s.contains(":") && !s.hasPrefix("[") {
            if let i = s.lastIndex(of: ":") {
                s = String(s[..<i])
            }
        }
        return s
    }

    /// Point-to-point peer: last octet 1↔2.
    static func peer4(_ ip: String) -> String {
        let p = ip.split(separator: ".").compactMap { UInt32($0) }
        guard p.count == 4 else { return ip }
        let last: UInt32 = p[3] == 1 ? 2 : 1
        return "\(p[0]).\(p[1]).\(p[2]).\(last)"
    }

    /// Current default gateway from `route get default`, else netstat.
    static func gateway() -> String {
        let p = Process()
        p.launchPath = "/sbin/route"
        p.arguments = ["-n", "get", "default"]
        let out = Pipe()
        p.standardOutput = out
        p.standardError = Pipe()
        p.launch()
        p.waitUntilExit()
        let s = String(data: out.fileHandleForReading.readDataToEndOfFile(), encoding: .utf8) ?? ""
        var gw = ""
        for raw in s.components(separatedBy: "\n") {
            let line = raw.trimmingCharacters(in: .whitespaces)
            if line.hasPrefix("gateway:") {
                gw = line.replacingOccurrences(of: "gateway:", with: "").trimmingCharacters(in: .whitespaces)
            }
        }
        if isRealGW(gw) { return gw }
        return netstatDefault()
    }

    /// First non-utun default from `netstat -rn`, preferring `en*`.
    private static func netstatDefault() -> String {
        let p = Process()
        p.launchPath = "/usr/sbin/netstat"
        p.arguments = ["-rn", "-f", "inet"]
        let out = Pipe()
        p.standardOutput = out
        p.standardError = Pipe()
        p.launch()
        p.waitUntilExit()
        let s = String(data: out.fileHandleForReading.readDataToEndOfFile(), encoding: .utf8) ?? ""
        var fallback = ""
        for raw in s.components(separatedBy: "\n") {
            let parts = raw.split(separator: " ", omittingEmptySubsequences: true).map(String.init)
            guard parts.count >= 4, parts[0] == "default" else { continue }
            let gw = parts[1]
            let iface = parts[parts.count - 1]
            if iface.hasPrefix("utun") || !isRealGW(gw) { continue }
            if iface.hasPrefix("en") { return gw }
            if fallback.isEmpty { fallback = gw }
        }
        return fallback
    }

    /// IPv4 addresses for `host` via getaddrinfo.
    static func resolve(_ host: String) -> [String] {
        if host.isEmpty { return [] }
        var hints = addrinfo(ai_flags: 0, ai_family: AF_INET, ai_socktype: 0, ai_protocol: 0, ai_addrlen: 0, ai_canonname: nil, ai_addr: nil, ai_next: nil)
        var res: UnsafeMutablePointer<addrinfo>?
        guard getaddrinfo(host, nil, &hints, &res) == 0, let info = res else { return [] }
        defer { freeaddrinfo(info) }
        var out: [String] = []
        var p: UnsafeMutablePointer<addrinfo>? = info
        while let cur = p {
            if cur.pointee.ai_family == AF_INET, let a = cur.pointee.ai_addr {
                a.withMemoryRebound(to: sockaddr_in.self, capacity: 1) { sin in
                    var buf = [CChar](repeating: 0, count: Int(INET_ADDRSTRLEN))
                    var addr = sin.pointee.sin_addr
                    inet_ntop(AF_INET, &addr, &buf, socklen_t(INET_ADDRSTRLEN))
                    out.append(String(cString: buf))
                }
            }
            p = cur.pointee.ai_next
        }
        return out
    }
}
