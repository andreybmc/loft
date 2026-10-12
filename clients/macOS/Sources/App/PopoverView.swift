// Status-item popover: connect/disconnect, profile rows, spark line, and footer links.

import AppKit

/// Status-item popover: connect/disconnect, profile rows, spark line, and footer links.
final class PopoverView: FlippedView {
    var onToggle: (() -> Void)?
    var onToggleID: ((String) -> Void)?
    var onOpen: (() -> Void)?
    var onQuit: (() -> Void)?
    var onSwitch: (() -> Void)?
    var onReset: (() -> Void)?
    var onJournal: (() -> Void)?
    var onSettings: (() -> Void)?

    private let mark = MarkView(frame: .zero)
    private let titleLab = NSTextField(labelWithString: "")
    private let subDot = FlippedView(frame: .zero)
    private let subLab = NSTextField(labelWithString: "")
    private let actionBtn = PillButton(frame: .zero)
    private let resetBtn = PillButton(frame: .zero)
    private let kConn = NSTextField(labelWithString: "")
    private let vConn = GhostButton(frame: .zero)
    private let kOver = NSTextField(labelWithString: "Overlay")
    private let vOver = NSTextField(labelWithString: "")
    private let tunBadge = LampTag(frame: .zero)
    private let kRoute = NSTextField(labelWithString: "")
    private let vRoute = NSTextField(labelWithString: "")
    private let kSocks = NSTextField(labelWithString: "SOCKS5")
    private let vSocks = NSTextField(labelWithString: "")
    private let sparkL = MarqueeLab(frame: .zero)
    private let sparkR = NSTextField(labelWithString: "")
    private let rateUp = NSTextField(labelWithString: "")
    private let rateDown = NSTextField(labelWithString: "")
    private let openBtn = GhostButton(frame: .zero)
    private let journalBtn = GhostButton(frame: .zero)
    private let quitBtn = GhostButton(frame: .zero)
    private let line = FlippedView(frame: .zero)
    private let connHit = ActionButton(frame: .zero)
    private let multiBox = FlippedView(frame: .zero)
    private var showRows = true
    private var showMulti = false
    private var showNet = false
    private var showReset = false
    private var showRates = false
    private var showSocks = false
    private var showTun = false

    /// Lets a first click on the popover fire without an extra focus click.
    override func acceptsFirstMouse(for event: NSEvent?) -> Bool { true }

    /// Builds the popover chrome: mark, action, rows, spark, and footer links.
    override init(frame frameRect: NSRect) {
        super.init(frame: NSRect(x: 0, y: 0, width: 300, height: 280))
        wantsLayer = true
        layer?.backgroundColor = Palette.popFill.cgColor
        layer?.cornerRadius = 12
        layer?.masksToBounds = true

        titleLab.font = Palette.body(13)
        titleLab.textColor = Palette.snow
        subDot.wantsLayer = true
        subDot.layer?.cornerRadius = 3.5
        subLab.font = Palette.body(12)
        subLab.textColor = Palette.mute

        actionBtn.target = self
        actionBtn.action = #selector(tapToggle)
        resetBtn.target = self
        resetBtn.action = #selector(tapReset)

        for lab in [kConn, kOver, kRoute, kSocks, vOver, vRoute, vSocks, titleLab, subLab, sparkR, rateUp, rateDown] {
            lab.usesSingleLineMode = true
            lab.lineBreakMode = .byTruncatingTail
            lab.cell?.wraps = false
            lab.cell?.truncatesLastVisibleLine = true
        }
        for lab in [kConn, kOver, kRoute, kSocks] {
            lab.font = Palette.body(12.5)
            lab.textColor = Palette.mute
        }
        for lab in [vOver, vRoute, vSocks] {
            lab.font = Palette.mono(11.5)
            lab.textColor = Palette.snow
            lab.alignment = .right
        }
        tunBadge.isHidden = true
        vConn.font = Palette.mono(11.5)
        vConn.tone = Palette.stone
        vConn.alignment = .right
        vConn.target = self
        vConn.action = #selector(tapSwitch)
        sparkL.font = Palette.body(11.5)
        sparkL.textColor = Palette.mute
        sparkR.font = Palette.body(11.5)
        sparkR.textColor = Palette.mute
        sparkR.alignment = .right
        for lab in [rateUp, rateDown] {
            lab.font = Palette.body(11.5)
            lab.textColor = Palette.mute
            lab.alignment = .right
        }

        line.wantsLayer = true
        line.layer?.backgroundColor = Palette.line.cgColor

        openBtn.font = Palette.body(12.5)
        openBtn.tone = Palette.stone
        openBtn.alignment = .left
        openBtn.target = self
        openBtn.action = #selector(tapOpen)

        quitBtn.font = Palette.body(12.5)
        quitBtn.tone = Palette.stone
        quitBtn.alignment = .right
        quitBtn.target = self
        quitBtn.action = #selector(tapQuit)

        journalBtn.font = Palette.body(12.5)
        journalBtn.tone = Palette.stone
        journalBtn.alignment = .center
        journalBtn.target = self
        journalBtn.action = #selector(tapSettings)

        connHit.title = ""
        connHit.drawsWash = false
        connHit.target = self
        connHit.action = #selector(tapSwitch)

        // connHit under the row so the value can still hover as a link.
        for v in [mark, titleLab, subDot, subLab, rateUp, rateDown, actionBtn, resetBtn, connHit, kConn, vConn, kOver, tunBadge, vOver, kRoute, vRoute, kSocks, vSocks, sparkL, sparkR, multiBox, line, openBtn, journalBtn, quitBtn] as [NSView] {
            addSubview(v)
        }
    }

    /// Inside 1pt popover outline.
    override func draw(_ dirtyRect: NSRect) {
        loftStrokeInside(bounds, radius: 12, color: NSColor(white: 1, alpha: 0.08))
    }

    /// Storyboard path is unused.
    required init?(coder: NSCoder) { fatalError() }

    /// Relays session state into labels and returns the fitted popover size.
    func render(engine: SessionController) -> NSSize {
        let p = engine.store.active()
        let name: String
        if let p = p, !p.name.isEmpty { name = p.name }
        else if let p = p { name = Palette.hostOf(p.url) }
        else { name = "—" }
        let simple = engine.store.simple
        let multi = engine.store.multi && engine.store.profiles.count > 1
        let held = engine.holdArmed
        let on = multi ? engine.anyOn : engine.state == .on
        let fail = multi ? (engine.anyError && !engine.anyOn) : engine.state == .error
        let connecting = multi
            ? engine.store.profiles.contains(where: { engine.state(of: $0.id) == .connecting })
            : engine.state == .connecting
        let disconnecting = multi
            ? engine.store.profiles.contains(where: { engine.state(of: $0.id) == .disconnecting })
            : engine.state == .disconnecting
        let busy = connecting || disconnecting
        showMulti = multi
        showRows = !simple && !multi
        showNet = on && !held && !fail && !multi
        showReset = held

        var markState: MarkState = .off
        if held || fail { markState = .err }
        else if on { markState = .on }
        mark.state = markState

        if multi {
            let n = engine.liveCount
            if n > 0 && engine.anyError {
                titleLab.stringValue = String(format: L10n.t("open_n_err"), n)
            } else if n > 0 {
                titleLab.stringValue = String(format: L10n.t("open_n"), n)
            } else if engine.anyError {
                titleLab.stringValue = L10n.t("err")
            } else {
                titleLab.stringValue = L10n.t("pop_off")
            }
        } else if on, let from = engine.connectedAt {
            titleLab.stringValue = "\(L10n.t("pill_on"))  \(Palette.uptime(from))"
        } else {
            titleLab.stringValue = on ? L10n.t("pill_on") : L10n.t("pop_off")
        }
        titleLab.textColor = Palette.snow
        subLab.stringValue = name
        subLab.textColor = (held || fail) ? Palette.err : Palette.mute
        subDot.layer?.backgroundColor = ((held || fail) ? Palette.err : (on ? Palette.lamp : Palette.mute)).cgColor

        actionBtn.lampOn = on && !held && !fail && !busy
        actionBtn.isEnabled = !disconnecting
        actionBtn.title = engine.actionTitle()
        actionBtn.isHidden = multi
        resetBtn.title = L10n.t("reset")
        resetBtn.lampOn = false
        resetBtn.isHidden = !showReset

        kConn.stringValue = L10n.t("conn")
        vConn.title = name + "\u{2004}›"
        kOver.stringValue = L10n.t("overlay")
        kRoute.stringValue = L10n.t("route")

        let socksLive = engine.socksDisplay
        showSocks = showNet && !socksLive.isEmpty
        showTun = showNet && engine.tunLive
        vSocks.stringValue = showSocks ? socksLive : ""
        vRoute.stringValue = showNet ? engine.routeCaption : ""
        vRoute.textColor = Palette.snow
        vOver.stringValue = showNet ? (engine.overlayIP.isEmpty ? "—" : engine.overlayIP) : ""
        vOver.textColor = Palette.snow

        let spark = engine.sparkLine()
        showRates = spark.pair
        let upStr = "↑ \(Palette.rate(engine.upRate, bits: engine.store.rateBits))"
        let downStr = "↓ \(Palette.rate(engine.downRate, bits: engine.store.rateBits))"
        rateUp.stringValue = showRates ? upStr : ""
        rateDown.stringValue = showRates ? downStr : ""
        sparkL.textColor = spark.color
        sparkR.stringValue = ""

        openBtn.title = L10n.t("open")
        journalBtn.title = L10n.t("settings")
        quitBtn.title = L10n.t("quit")

        let pad: CGFloat = 14
        let w: CGFloat = 300
        var y: CGFloat = pad
        mark.frame = NSRect(x: pad, y: y, width: 36, height: 36)
        let textX = pad + 46
        let rateFont = Palette.body(11.5)
        let rateW: CGFloat = showRates
            ? max(64, Palette.textWidth(upStr, font: rateFont), Palette.textWidth(downStr, font: rateFont))
            : 0
        let rateGap: CGFloat = showRates ? 8 : 0
        let textW = w - textX - pad - rateW - rateGap
        titleLab.frame = NSRect(x: textX, y: y + 1, width: textW, height: 18)
        let subY: CGFloat = y + 20
        let subH: CGFloat = 16
        let dotS: CGFloat = 7
        subDot.frame = NSRect(x: textX, y: subY + round((subH - dotS) / 2) + 3, width: dotS, height: dotS)
        subLab.frame = NSRect(x: textX + 11, y: subY, width: max(20, textW - 11), height: subH)
        rateUp.frame = NSRect(x: w - pad - rateW, y: y + 1, width: rateW, height: 18)
        rateDown.frame = NSRect(x: w - pad - rateW, y: subY, width: rateW, height: subH)
        rateUp.isHidden = !showRates
        rateDown.isHidden = !showRates
        y += 36 + 14
        if !multi {
            actionBtn.frame = NSRect(x: pad, y: y, width: w - pad * 2, height: 40)
            y += 40
        }
        if showReset {
            y += 6
            resetBtn.frame = NSRect(x: pad, y: y, width: w - pad * 2, height: 40)
            y += 40
        }
        if showRows {
            y += 12
            let inset: CGFloat = pad + 2
            let keyW: CGFloat = 118
            let valX = inset + keyW
            let valW = w - inset - valX
            let rowH: CGFloat = 28
            let labH: CGFloat = 18
            let labY = { (rowY: CGFloat) in rowY + round((rowH - labH) / 2) }
            /// Places a mute key label on a 28pt row.
            func placeKey(_ k: NSTextField, _ rowY: CGFloat) {
                k.frame = NSRect(x: inset, y: labY(rowY), width: keyW, height: labH)
                k.isHidden = false
            }
            let connY = y
            placeKey(kConn, connY)
            vConn.frame = NSRect(x: valX, y: labY(connY), width: valW, height: labH)
            vConn.isHidden = false
            connHit.frame = NSRect(x: pad, y: connY, width: w - pad * 2, height: rowH)
            connHit.isHidden = false
            y += rowH
            /// Places a key/value row and advances `y`.
            func row(_ k: NSTextField, _ v: NSTextField) {
                placeKey(k, y)
                v.frame = NSRect(x: valX, y: labY(y), width: valW, height: labH)
                v.isHidden = false
                y += rowH
            }
            if showNet {
                let overY = y
                let keyFont = kOver.font ?? Palette.body(12.5)
                let overW = Palette.textWidth(kOver.stringValue, font: keyFont)
                kOver.frame = NSRect(x: inset, y: labY(overY), width: overW, height: labH)
                kOver.isHidden = false
                if showTun {
                    let tag = L10n.t("net_tag")
                    tunBadge.text = tag
                    let sz = LampTag.size(for: tag)
                    tunBadge.frame = NSRect(
                        x: inset + overW + 6,
                        y: overY + round((rowH - sz.height) / 2),
                        width: sz.width,
                        height: sz.height
                    )
                    tunBadge.isHidden = false
                } else {
                    tunBadge.isHidden = true
                }
                vOver.frame = NSRect(x: valX, y: labY(overY), width: valW, height: labH)
                vOver.isHidden = false
                y += rowH
                row(kRoute, vRoute)
            } else {
                kOver.isHidden = true
                vOver.isHidden = true
                kRoute.isHidden = true
                vRoute.isHidden = true
                tunBadge.isHidden = true
            }
            if showSocks {
                row(kSocks, vSocks)
            } else {
                kSocks.isHidden = true
                vSocks.isHidden = true
            }
            y += 4
            sparkL.frame = NSRect(x: inset, y: y, width: w - inset * 2, height: 16)
            sparkR.isHidden = true
            sparkL.isHidden = false
            sparkL.show(spark.text, scroll: spark.scroll, keepMoving: connecting || disconnecting)
            y += 24
            multiBox.isHidden = true
        } else if multi {
            for v in [kConn, vConn, kOver, vOver, tunBadge, kRoute, vRoute, kSocks, vSocks, sparkR, connHit] as [NSView] {
                v.isHidden = true
            }
            y += 8
            let listH = fillMulti(engine, width: w - pad * 2)
            multiBox.frame = NSRect(x: pad, y: y, width: w - pad * 2, height: listH)
            multiBox.isHidden = false
            y += listH + 8
            sparkL.frame = NSRect(x: pad + 2, y: y, width: w - pad * 2 - 4, height: 16)
            sparkL.isHidden = simple
            if simple {
                sparkL.show("", scroll: false)
            } else {
                sparkL.show(spark.text, scroll: spark.scroll, keepMoving: connecting || disconnecting)
                y += 24
            }
        } else {
            for v in [kConn, vConn, kOver, vOver, tunBadge, kRoute, vRoute, kSocks, vSocks, sparkL, sparkR, connHit] as [NSView] {
                v.isHidden = true
            }
            sparkL.show("", scroll: false)
            multiBox.isHidden = true
            y += 8
        }
        line.frame = NSRect(x: pad, y: y, width: w - pad * 2, height: 1)
        y += 8
        /// Width of a ghost title, at least 32pt.
        func ghostW(_ b: GhostButton) -> CGFloat {
            max(32, Palette.textWidth(b.title, font: b.font ?? Palette.body(12.5)))
        }
        let oW = ghostW(openBtn)
        let jW = ghostW(journalBtn)
        let qW = ghostW(quitBtn)
        openBtn.frame = NSRect(x: pad, y: y, width: oW, height: 22)
        quitBtn.frame = NSRect(x: w - pad - qW, y: y, width: qW, height: 22)
        let minJ = pad + oW + 8
        let maxJ = w - pad - qW - 8 - jW
        let midJ = round((w - jW) / 2)
        let jX = maxJ >= minJ ? min(max(midJ, minJ), maxJ) : midJ
        journalBtn.frame = NSRect(x: jX, y: y, width: jW, height: 22)
        y += 22 + 10
        let h = y
        frame = NSRect(x: 0, y: 0, width: w, height: h)
        return NSSize(width: w, height: h)
    }

    /// One Open/Close row per profile. Returns the stacked height.
    private func fillMulti(_ engine: SessionController, width: CGFloat) -> CGFloat {
        multiBox.subviews.forEach { $0.removeFromSuperview() }
        let actFont = Palette.font(11.5)
        let actTitles = [L10n.t("pop_connect"), L10n.t("disconnect"), L10n.t("connecting"), L10n.t("retry"), L10n.t("cancel")]
        let actW = max(72, (actTitles.map { Palette.textWidth($0, font: actFont) }.max() ?? 72) + 18)
        let actH: CGFloat = 26
        let simple = engine.store.simple
        var y: CGFloat = 0
        for p in engine.store.profiles {
            let rowH: CGFloat = simple ? 36 : 44
            let st = engine.state(of: p.id)
            let live = st == .on
            let row = LoftPlate(frame: NSRect(x: 0, y: y, width: width, height: rowH))
            row.radius = 10
            if live || st == .connecting || st == .error {
                row.fill = Palette.field
                row.stroke = Palette.slate
            }
            let act = PillButton(frame: NSRect(x: 6, y: round((rowH - actH) / 2), width: actW, height: actH))
            act.compact = true
            act.identifier = NSUserInterfaceItemIdentifier(p.id)
            act.target = self
            act.action = #selector(tapToggleID(_:))
            act.title = engine.actionTitle(of: p.id)
            act.lampOn = live
            act.isEnabled = st != .disconnecting
            let textX = 6 + actW + 8
            let name = p.name.isEmpty ? Palette.hostOf(p.url) : p.name
            let n = Palette.label(name, font: Palette.font(12.5), color: Palette.snow, frame: NSRect(x: textX, y: simple ? 9 : 6, width: max(40, width - textX - 8), height: 16))
            row.addSubview(act)
            row.addSubview(n)
            if !simple {
                var sub = ""
                if st == .error {
                    sub = engine.errorText(of: p.id)
                    if sub.isEmpty { sub = L10n.t("fail") }
                } else if live {
                    let ip = engine.overlayIP(of: p.id)
                    let socks = engine.socksDisplay(of: p.id)
                    if !ip.isEmpty { sub = ip }
                    if !socks.isEmpty {
                        let port = Palette.socksPort(socks)
                        sub = sub.isEmpty ? port : sub + " · " + port
                    }
                    if sub.isEmpty { sub = L10n.t("conn_up") }
                } else {
                    sub = Palette.hostOf(p.url)
                }
                let u = Palette.label(sub, font: Palette.mono(11), color: st == .error ? Palette.err : Palette.mute, frame: NSRect(x: textX, y: 22, width: max(40, width - textX - 8), height: 14))
                row.addSubview(u)
            }
            multiBox.addSubview(row)
            y += rowH + 4
        }
        return max(0, y - 4)
    }

    /// Connect/disconnect (or retry) from the main pill.
    @objc private func tapToggle() {
        LoftLog.shared.tap("popover", actionBtn.title)
        onToggle?()
    }
    /// Open/Close one profile in the multi list.
    @objc private func tapToggleID(_ sender: NSButton) {
        let id = sender.identifier?.rawValue ?? ""
        if id.isEmpty { return }
        LoftLog.shared.tap("popover", id)
        onToggleID?(id)
    }
    /// Opens the status window.
    @objc private func tapOpen() { onOpen?() }
    /// Quits the app.
    @objc private func tapQuit() { onQuit?() }
    /// Restores pre-loft routes after a hold.
    @objc private func tapReset() { onReset?() }
    /// Opens the connections list.
    @objc private func tapSwitch() { onSwitch?() }
    /// Opens the journal (wired if a journal footer is shown).
    @objc private func tapJournal() { onJournal?() }
    /// Opens settings. The center footer button currently calls this.
    @objc private func tapSettings() { onSettings?() }
}
