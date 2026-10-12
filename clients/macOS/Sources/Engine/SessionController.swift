// Session owner: loft-front via FrontHelper, TUN via loft-priv, reconnect, and UI state.
// Several outbounds (`prefs.multi`): one LiveSession per profile, each with its own loft-front and utun.

import AppKit
import Foundation

extension Notification.Name {
    /// Posted on the main queue when connect state, overlay, or prefs change.
    static let loftState = Notification.Name("loft.state")
}

/// Per-profile outbound: loft-front process, overlay, optional utun.
final class LiveSession {
    let id: String
    var state: SessionController.State = .off
    var errorText = ""
    var overlayIP = ""
    var frontName = ""
    var tunLive = false
    var defaultArmed = false
    var socksDisplay = ""
    var upRate = 0
    var downRate = 0
    var connectedAt: Date?
    var progressText = ""
    var progressParts: [String] = []
    let helper = FrontHelper()
    var tunArmed = false
    var tunURL = ""
    var tunName = ""
    var generation = 0
    var reconnectWork: DispatchWorkItem?
    var reconnectAttempt = 0
    var reconnectAt: Date?

    init(id: String) { self.id = id }
}

/// Session owner: loft-front via FrontHelper, TUN via loft-priv, reconnect, and UI state.
final class SessionController {
    /// Connect lifecycle shown in the popover and status window.
    enum State: String {
        case off, connecting, disconnecting, on, error
    }

    let store = ProfileStore()
    private var lives: [String: LiveSession] = [:]
    private var front: GETFront?
    private let tun = TUNEngine()
    private let socks = SOCKS5Server()
    private let queue = DispatchQueue(label: "app.loft.session")
    private var lastFail = ""

    /// `"ws"` for ws(s) URLs, otherwise `"get"`.
    static func kindForURL(_ url: String) -> String {
        let s = url.trimmingCharacters(in: .whitespacesAndNewlines).lowercased()
        if s.hasPrefix("ws://") || s.hasPrefix("wss://") { return "ws" }
        return "get"
    }

    var software: String {
        (Bundle.main.object(forInfoDictionaryKey: "CFBundleShortVersionString") as? String) ?? "0.1.0"
    }

    /// Active profile's session, or an empty off session.
    private var activeLive: LiveSession? { lives[store.activeID] }

    var state: State { state(of: store.activeID) }
    var errorText: String { activeLive?.errorText ?? "" }
    var overlayIP: String { activeLive?.overlayIP ?? "" }
    var frontName: String { activeLive?.frontName ?? "" }
    var tunLive: Bool { activeLive?.tunLive ?? false }
    var defaultArmed: Bool { lives.values.contains { $0.defaultArmed } }
    var socksDisplay: String { activeLive?.socksDisplay ?? "" }
    var upRate: Int {
        if store.multi { return lives.values.reduce(0) { $0 + $1.upRate } }
        return activeLive?.upRate ?? 0
    }
    var downRate: Int {
        if store.multi { return lives.values.reduce(0) { $0 + $1.downRate } }
        return activeLive?.downRate ?? 0
    }
    var connectedAt: Date? { activeLive?.connectedAt }
    var progressText: String { activeLive?.progressText ?? "" }

    /// True when any outbound is up.
    var anyOn: Bool { lives.values.contains { $0.state == .on } }
    /// True when any outbound is in error.
    var anyError: Bool { lives.values.contains { $0.state == .error } }
    /// Count of live outbounds.
    var liveCount: Int { lives.values.filter { $0.state == .on }.count }

    /// Loads profiles, installs loft-priv if needed, and reaps stale holders.
    func boot() {
        store.load()
        try? Priv.ensureSetuid()
        FrontHelper.reapOrphans()
        Priv.reapStale()
    }

    /// Session for `id`, creating a slot if needed.
    private func ensure(_ id: String) -> LiveSession {
        if let s = lives[id] { return s }
        let s = LiveSession(id: id)
        lives[id] = s
        return s
    }

    /// Connect lifecycle of one profile.
    func state(of id: String) -> State { lives[id]?.state ?? .off }
    /// Overlay IP of one profile, or empty.
    func overlayIP(of id: String) -> String { lives[id]?.overlayIP ?? "" }
    /// SOCKS bind of one live profile, or empty.
    func socksDisplay(of id: String) -> String { lives[id]?.socksDisplay ?? "" }
    /// TUN lamp for one profile.
    func tunLive(of id: String) -> Bool { lives[id]?.tunLive ?? false }
    /// Error line for one profile.
    func errorText(of id: String) -> String { lives[id]?.errorText ?? "" }

    /// Ports already bound by live loft-front SOCKS listeners.
    private func takenSocks(except: String = "") -> Set<UInt16> {
        var out = Set<UInt16>()
        for s in lives.values where s.id != except && (s.state == .on || s.state == .connecting) {
            let p = Palette.socksPort(s.socksDisplay)
            if let u = UInt16(p), !s.socksDisplay.isEmpty { out.insert(u) }
        }
        return out
    }

    /// Other live/connecting sessions besides `id`.
    private func othersBusy(besides id: String) -> [LiveSession] {
        lives.values.filter { $0.id != id && ($0.state == .on || $0.state == .connecting || $0.state == .disconnecting) }
    }

    /// Starts loft-front, then overlay TUN; default route only if SOCKS TCP works.
    func connect() { connect(nil) }

    /// Opens one profile. Exclusive mode tears the others first.
    func connect(_ id: String?) {
        let pid = (id?.isEmpty == false) ? id! : store.activeID
        guard let p0 = store.profiles.first(where: { $0.id == pid }) ?? store.active() else {
            fail(L10n.t("none"))
            return
        }
        var p = p0
        if !store.multi {
            store.select(p.id)
            let leftover = othersBusy(besides: p.id)
            if !leftover.isEmpty {
                for s in leftover { s.generation += 1 }
                set(p.id, .disconnecting, err: "", ip: overlayIP(of: p.id))
                queue.async {
                    for s in leftover { self.tearDown(s) }
                    DispatchQueue.main.async { self.connect(p.id) }
                }
                return
            }
        }
        let s = ensure(p.id)
        cancelReconnect(s)
        if s.state == .connecting { return }
        if p.url.isEmpty {
            fail(L10n.t("need_key"), id: p.id)
            return
        }
        if p.isSSTP {
            if p.user.isEmpty || p.token.isEmpty {
                fail(L10n.t("need_sstp"), id: p.id)
                return
            }
        } else if p.token.isEmpty {
            fail(L10n.t("need_key"), id: p.id)
            return
        }
        if !p.tun && !p.socksOn {
            fail(L10n.t("need_inbound"), id: p.id)
            return
        }
        s.generation += 1
        let ticket = s.generation
        set(p.id, .connecting, err: "", ip: "")
        queue.async {
            if ticket != s.generation { return }
            s.helper.stop()
            do {
                if p.kind.isEmpty { p.kind = Self.kindForURL(p.url) }
                if p.isSSTP {
                    LoftLog.shared.add("loft", "open \(p.name) sstp \(p.url)")
                } else {
                    LoftLog.shared.add("loft", "open \(p.name) \(p.kind) \(p.url) path=\(p.path)")
                }
                s.helper.onLine = { [weak self] line in
                    guard let self = self else { return }
                    guard let tok = FrontExplain.stageToken(from: line) else { return }
                    DispatchQueue.main.async {
                        guard s.state == .connecting else { return }
                        self.appendStage(s, tok)
                        NotificationCenter.default.post(name: .loftState, object: self)
                    }
                }
                s.helper.onExit = { [weak self] code in
                    guard let self = self else { return }
                    DispatchQueue.main.async {
                        if s.state == .on || s.state == .connecting {
                            LoftLog.shared.add("loft", "front died \(code) id=\(p.id)")
                            self.queue.async {
                                self.tearDown(s, died: true)
                                DispatchQueue.main.async {
                                    self.set(p.id, .error, err: FrontExplain.display("front \(code)"), ip: "")
                                    self.scheduleReconnect(s)
                                }
                            }
                        }
                    }
                }
                let socksAddr = self.store.socksBind(for: p, taken: self.takenSocks(except: p.id))
                try s.helper.start(profile: p, socksBind: socksAddr, instanceFile: self.store.instanceFile(for: p.id))
                if ticket != s.generation { return }
                s.frontName = p.kind
                let ip = s.helper.overlayIP
                s.socksDisplay = socksAddr
                s.tunLive = false
                s.tunArmed = false
                s.defaultArmed = false
                s.tunURL = p.url
                LoftLog.shared.add("loft", "on id=\(p.id) overlay=\(ip) socks=\(socksAddr)")
                self.set(p.id, .on, err: "", ip: ip)
                if p.tun, !ip.isEmpty, !s.helper.tunSock.isEmpty {
                    do {
                        let takeDefault = self.store.defaultRoute && !self.lives.values.contains(where: { $0.id != p.id && $0.defaultArmed })
                        try Priv.upTUN(sock: s.helper.tunSock, ip: ip, url: p.url, defaultRoute: false, mtu: s.helper.overlayMTU, key: p.id)
                        s.helper.waitTun(seconds: 8)
                        if ticket != s.generation { return }
                        s.tunLive = true
                        s.tunArmed = true
                        s.tunName = Priv.tunName(for: p.id)
                        s.defaultArmed = false
                        if takeDefault {
                            if socksAddr.isEmpty {
                                LoftLog.shared.add("loft", "tun default skipped: no socks to probe tcp")
                            } else if Priv.loftCarriesTCP(socksAddr) {
                                Priv.setDefaultRoute(true, url: p.url, overlayIP: ip, ifname: s.tunName)
                                s.defaultArmed = true
                            } else {
                                LoftLog.shared.add("loft", L10n.t("err_no_tcp"))
                            }
                        }
                        LoftLog.shared.add("loft", "tun live id=\(p.id) if=\(s.tunName) default=\(s.defaultArmed ? 1 : 0)")
                        LoftLog.shared.snapshot("live")
                    } catch {
                        LoftLog.shared.add("loft", "tun: \(error.localizedDescription)")
                    }
                    if ticket != s.generation { return }
                    self.set(p.id, .on, err: "", ip: ip)
                }
            } catch {
                if ticket != s.generation { return }
                let raw = error.localizedDescription
                if raw == "cancelled" { return }
                s.helper.stop()
                let shown = FrontExplain.display(raw)
                LoftLog.shared.add("loft", "error \(shown)")
                self.set(p.id, .error, err: shown, ip: "")
                self.scheduleReconnect(s)
            }
        }
    }

    /// Makes `id` active. Exclusive live session reconnects onto it; multi only highlights.
    func selectProfile(_ id: String) {
        if id.isEmpty || id == store.activeID { return }
        store.select(id)
        if store.multi {
            NotificationCenter.default.post(name: .loftState, object: self)
            return
        }
        if anyOn || lives.values.contains(where: { $0.state == .connecting }) {
            reconnectAfterClose(to: id)
            return
        }
        NotificationCenter.default.post(name: .loftState, object: self)
    }

    /// Turns Several outbounds on or off. Off keeps only the active session.
    func setMulti(_ on: Bool) {
        store.multi = on
        store.savePrefs()
        if !on {
            let keep = store.activeID
            for s in lives.values where s.id != keep {
                if s.state == .on || s.state == .connecting || s.state == .error {
                    disconnect(s.id)
                }
            }
        }
        NotificationCenter.default.post(name: .loftState, object: self)
    }

    /// User disconnect: cancel reconnect and tear down on the session queue.
    func disconnect() { disconnect(nil) }

    /// Closes one profile, or the active one.
    func disconnect(_ id: String?) {
        let pid = (id?.isEmpty == false) ? id! : store.activeID
        let s = lives[pid]
        if s == nil || s?.state == .off || s?.state == .disconnecting { return }
        cancelReconnect(s)
        if let s = s { s.reconnectAttempt = 0; s.generation += 1 }
        set(pid, .disconnecting, err: "", ip: overlayIP(of: pid))
        queue.async {
            if let s = s { self.tearDown(s) }
            self.set(pid, .off, err: "", ip: "")
        }
    }

    /// Quit path: tearDown is async on the session queue; wait so loft-priv dies.
    func halt() {
        for s in lives.values {
            cancelReconnect(s)
            s.reconnectAttempt = 0
            s.generation += 1
        }
        let sem = DispatchSemaphore(value: 0)
        queue.async {
            for s in self.lives.values { self.tearDown(s) }
            DispatchQueue.main.async {
                for s in self.lives.values { self.set(s.id, .off, err: "", ip: "") }
            }
            sem.signal()
        }
        _ = sem.wait(timeout: .now() + 8)
        FrontHelper.reapOrphans()
        Priv.stopHeld(key: nil)
    }

    /// Cancels every pending reconnect timer.
    func cancelReconnect() {
        for s in lives.values { cancelReconnect(s) }
    }

    /// Cancels a pending reconnect timer for one session.
    private func cancelReconnect(_ s: LiveSession?) {
        s?.reconnectWork?.cancel()
        s?.reconnectWork = nil
        s?.reconnectAt = nil
    }

    /// Seconds left on the reconnect countdown of the active profile, or nil.
    var reconnectLeft: Int? { reconnectLeft(of: store.activeID) }

    /// Seconds left on the reconnect countdown, or nil if none.
    func reconnectLeft(of id: String) -> Int? {
        guard let s = lives[id], s.reconnectWork != nil, s.state == .error, let at = s.reconnectAt else { return nil }
        return max(1, Int(ceil(at.timeIntervalSinceNow)))
    }

    /// Caption for the main connect pill (active profile).
    func actionTitle() -> String { actionTitle(of: store.activeID) }

    /// Open / Disconnect / Retry caption for one profile.
    func actionTitle(of id: String) -> String {
        switch state(of: id) {
        case .on: return L10n.t("disconnect")
        case .disconnecting: return L10n.t("disconnecting")
        case .connecting: return L10n.t("cancel")
        case .error:
            if let n = reconnectLeft(of: id) {
                return String(format: L10n.t("retry_wait"), n)
            }
            return L10n.t("retry")
        case .off: return L10n.t("pop_connect")
        }
    }

    /// Front died or handshake threw. Retry with 1/2/4/8/15s backoff while the pref is on.
    private func scheduleReconnect(_ s: LiveSession) {
        let go = {
            guard self.store.reconnect else { return }
            guard s.state == .error else { return }
            s.reconnectWork?.cancel()
            let delay = min(15.0, 3.0 * pow(2.0, Double(min(s.reconnectAttempt, 2))))
            s.reconnectAttempt += 1
            s.reconnectAt = Date().addingTimeInterval(delay)
            LoftLog.shared.add("loft", "reconnect in \(Int(delay))s id=\(s.id)")
            let ticket = s.generation
            let work = DispatchWorkItem { [weak self] in
                guard let self = self else { return }
                guard ticket == s.generation else { return }
                guard self.store.reconnect else { return }
                self.connect(s.id)
            }
            s.reconnectWork = work
            DispatchQueue.main.asyncAfter(deadline: .now() + delay, execute: work)
            NotificationCenter.default.post(name: .loftState, object: self)
        }
        if Thread.isMainThread { go() } else { DispatchQueue.main.async(execute: go) }
    }

    /// Stops an in-flight handshake. UI goes off immediately; teardown is async.
    func cancelConnect() { cancelConnect(store.activeID) }

    /// Cancels an in-flight handshake for one profile.
    func cancelConnect(_ id: String) {
        guard let s = lives[id], s.state == .connecting else { return }
        cancelReconnect(s)
        s.reconnectAttempt = 0
        s.generation += 1
        LoftLog.shared.add("loft", "cancel connect id=\(id)")
        s.helper.interrupt()
        set(id, .off, err: "", ip: "")
        queue.async {
            s.helper.stop()
        }
    }

    /// Exclusive switch: close the live session, then open `id`.
    private func reconnectAfterClose(to id: String) {
        for s in lives.values {
            cancelReconnect(s)
            s.reconnectAttempt = 0
            s.generation += 1
        }
        set(store.activeID, .disconnecting, err: "", ip: overlayIP)
        queue.async {
            for s in self.lives.values { self.tearDown(s) }
            DispatchQueue.main.async { self.connect(id) }
        }
    }

    /// Stops loft-front first, then loft-priv for this session.
    private func tearDown(_ s: LiveSession, died: Bool = false) {
        LoftLog.shared.add("loft", "close id=\(s.id) armed=\(s.defaultArmed ? 1 : 0) hold=\(store.hold ? 1 : 0)")
        LoftLog.shared.snapshot("close")
        s.helper.onLine = nil
        s.helper.onExit = nil
        let ip = s.helper.overlayIP.isEmpty ? s.overlayIP : s.helper.overlayIP
        let others = lives.values.contains { $0.id != s.id && ($0.tunArmed || $0.state == .on) }
        if !died { s.helper.stop() }
        if s.tunArmed {
            let hold = store.hold && s.defaultArmed && !others
            Priv.downTUN(url: s.tunURL, hold: hold, ip: ip, restoreDefault: s.defaultArmed && !others, key: s.id, othersRemain: others)
            if s.defaultArmed, others, store.failover, store.defaultRoute {
                if let other = lives.values.first(where: { $0.id != s.id && $0.tunArmed && $0.state == .on }) {
                    if !other.socksDisplay.isEmpty, Priv.loftCarriesTCP(other.socksDisplay) {
                        Priv.setDefaultRoute(true, url: other.tunURL, overlayIP: other.overlayIP, ifname: other.tunName)
                        other.defaultArmed = true
                    }
                }
            }
        }
        s.tunArmed = false
        s.defaultArmed = false
        s.tunLive = false
        s.tunName = ""
        s.socksDisplay = ""
    }

    /// Asks loft-priv to restore pre-loft routes (and drop a hold).
    func resetRoutes() {
        queue.async {
            Priv.restoreRoutes()
            DispatchQueue.main.async {
                NotificationCenter.default.post(name: .loftState, object: self)
            }
        }
    }

    /// Applies or clears the 0/1 split default if TUN is live and TCP works.
    func syncDefaultRoute() {
        let on = store.defaultRoute
        queue.async {
            let owner = self.lives.values.first(where: { $0.tunArmed && $0.state == .on })
            if let s = owner {
                if on && (s.socksDisplay.isEmpty || !Priv.loftCarriesTCP(s.socksDisplay)) {
                    LoftLog.shared.add("loft", s.socksDisplay.isEmpty
                        ? "tun default skipped: no socks to probe tcp"
                        : L10n.t("err_no_tcp"))
                    s.defaultArmed = false
                    LoftLog.shared.snapshot("default-skip")
                } else {
                    Priv.setDefaultRoute(on, url: s.tunURL, overlayIP: s.overlayIP, ifname: s.tunName)
                    s.defaultArmed = on
                    for o in self.lives.values where o.id != s.id { o.defaultArmed = false }
                }
            }
            DispatchQueue.main.async {
                NotificationCenter.default.post(name: .loftState, object: self)
            }
        }
    }

    var tunName: String { activeLive?.tunName ?? tun.name }
    var holdArmed: Bool { tun.holdArmed }

    /// Machine path for this connection. SOCKS is a local inbound, not the route.
    var routeCaption: String { routeCaption(of: store.activeID) }

    /// Overlay/PPP gateway caption for one profile.
    func routeCaption(of id: String) -> String {
        if holdArmed { return L10n.t("held") }
        let tunOn = store.profiles.first(where: { $0.id == id })?.tun ?? false
        let wantDefault = tunOn && store.defaultRoute
        if state(of: id) == .on {
            let gw = overlayGatewayDisplay(of: id)
            if !gw.isEmpty { return gw }
            if lives[id]?.defaultArmed == true { return L10n.t("full") }
            if tunLive(of: id) { return L10n.t("net_tag") }
            return "SOCKS5"
        }
        if wantDefault { return L10n.t("full") }
        if tunOn { return L10n.t("net_tag") }
        return "SOCKS5"
    }

    /// Overlay/PPP gateway from STATUS, else .1 of the overlay /24.
    func overlayGatewayDisplay() -> String { overlayGatewayDisplay(of: store.activeID) }

    /// Overlay/PPP gateway for one profile.
    func overlayGatewayDisplay(of id: String) -> String {
        if let gw = lives[id]?.helper.overlayGW, !gw.isEmpty { return gw }
        return Self.slash24Gateway(overlayIP(of: id))
    }

    /// Typical overlay gateway: last octet 1, skipped when the address already is .1.
    static func slash24Gateway(_ ip: String) -> String {
        let p = ip.split(separator: ".")
        guard p.count == 4, p[3] != "1" else { return "" }
        return "\(p[0]).\(p[1]).\(p[2]).1"
    }

    /// Connects from off/error, disconnects from on, cancels an in-flight handshake.
    func toggle() { toggle(nil) }

    /// Same as `toggle()`, for one profile. Exclusive: selecting another row switches the session.
    func toggle(_ id: String?) {
        let pid = (id?.isEmpty == false) ? id! : store.activeID
        LoftLog.shared.tap("toggle", "\(pid) \(state(of: pid).rawValue)")
        if store.multi {
            switch state(of: pid) {
            case .on: disconnect(pid)
            case .off, .error: connect(pid)
            case .connecting: cancelConnect(pid)
            case .disconnecting: return
            }
            return
        }
        if pid != store.activeID {
            selectProfile(pid)
            if state == .off || state == .error { connect(pid) }
            return
        }
        switch state {
        case .on: disconnect()
        case .off, .error: connect()
        case .connecting: cancelConnect()
        case .disconnecting: return
        }
    }

    /// Opens a mux stream on the leftover GETFront, if one is running.
    func openStream(host: String, port: UInt16) -> LoftStream? {
        return front?.open(host: host, port: port)
    }

    /// Overlay IP reported by loft-front of the active profile.
    func helperIP() -> String { activeLive?.helper.overlayIP ?? "" }

    /// Copies loft-front byte counters into `upRate`/`downRate` once a second.
    func tickRates() {
        for s in lives.values {
            if s.state == .on {
                let t = s.helper.traffic()
                s.upRate = Int(t.0)
                s.downRate = Int(t.1)
            } else if s.upRate != 0 || s.downRate != 0 {
                s.upRate = 0
                s.downRate = 0
            }
        }
    }

    /// Bottom spark line: ticker while connecting, the real error on fail, front/proto/opts when on.
    func sparkLine() -> (text: String, color: NSColor, scroll: Bool, pair: Bool) {
        if holdArmed {
            return (L10n.t("held"), Palette.err, false, false)
        }
        if store.multi {
            if anyError && liveCount == 0 {
                let t = lives.values.first(where: { $0.state == .error })?.errorText
                return (t?.isEmpty == false ? t! : L10n.t("err"), Palette.err, true, false)
            }
            if anyOn {
                return (sessionSpark(), Palette.mute, true, true)
            }
            if lives.values.contains(where: { $0.state == .connecting }) {
                return (L10n.t("connecting"), Palette.mute, true, false)
            }
            return (L10n.t("none"), Palette.mute, false, false)
        }
        switch state {
        case .error:
            let t = errorText.isEmpty ? L10n.t("err") : errorText
            return (t, Palette.err, true, false)
        case .on:
            return (sessionSpark(), Palette.mute, true, true)
        case .connecting:
            let t = progressText.isEmpty ? L10n.t("connecting") : progressText
            return (t, Palette.mute, true, false)
        case .disconnecting:
            return (L10n.t("disconnecting"), Palette.mute, true, false)
        case .off:
            return (L10n.t("none"), Palette.mute, false, false)
        }
    }

    /// `lftp/1.0 · get · mux · mask` for loft; `sstp · ppp` for SSTP.
    func sessionSpark() -> String {
        if store.multi, liveCount > 1 {
            return String(format: L10n.t("open_n"), liveCount)
        }
        let front = frontName.isEmpty ? (store.active()?.kind ?? "get") : frontName
        if front == "sstp" {
            return tunLive ? "sstp  ·  ppp  ·  tun" : "sstp  ·  ppp"
        }
        let major = Int(LoftWire.proto >> 8)
        let minor = Int(LoftWire.proto & 0xff)
        let mux = (store.active()?.mux ?? true) ? "mux" : "connect"
        return "lftp/\(major).\(minor)  ·  \(front)  ·  \(mux)  ·  mask"
    }

    /// Appends a unique connecting-stage label to the spark ticker.
    private func appendStage(_ s: LiveSession, _ token: String) {
        let label = FrontExplain.stageLabel(token)
        if s.progressParts.contains(label) { return }
        s.progressParts.append(label)
        s.progressText = ([L10n.t("connecting")] + s.progressParts).joined(separator: "  ·  ")
    }

    /// Sets `.error` from a local fail (missing key/profile/inbound).
    private func fail(_ s: String, id: String? = nil) {
        LoftLog.shared.add("loft", "error \(s)")
        set(id ?? store.activeID, .error, err: FrontExplain.display(s), ip: "")
    }

    /// Publishes state on the main queue and posts `.loftState`.
    private func set(_ id: String, _ st: State, err: String, ip: String) {
        let apply = {
            let s = self.ensure(id)
            if st == .on {
                if s.state != .on { s.connectedAt = Date() }
                s.reconnectAttempt = 0
            } else if st != .disconnecting {
                s.connectedAt = nil
            }
            if st == .connecting {
                s.progressParts = [L10n.t("st_front")]
                s.progressText = L10n.t("connecting") + "  ·  " + L10n.t("st_front")
            } else if st != .disconnecting {
                s.progressParts = []
                s.progressText = ""
            }
            s.state = st
            s.errorText = err
            s.overlayIP = ip
            if st == .error && !err.isEmpty { self.lastFail = err }
            if st == .on { self.lastFail = "" }
            NotificationCenter.default.post(name: .loftState, object: self)
        }
        if Thread.isMainThread { apply() } else { DispatchQueue.main.async(execute: apply) }
    }
}
