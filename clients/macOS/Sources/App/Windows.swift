// Loft windows: status, connections list, settings, profile editor, and onboard.

import AppKit

/// Status window: mark, connect pill, overlay/front/SOCKS cards, spark, footer.
final class StatusWindowController: NSWindowController {
    private let engine: SessionController
    private let mark = MarkView(frame: .zero)
    private let titleLab = NSTextField(labelWithString: "loft")
    private let pillBox = FlippedView(frame: .zero)
    private let pillLab = NSTextField(labelWithString: "")
    private let dot = FlippedView(frame: .zero)
    private let pillHit = ActionButton(frame: .zero)
    private let action = PillButton(frame: .zero)
    private let sOverK = NSTextField(labelWithString: "OVERLAY")
    private let sOverV = NSTextField(labelWithString: "—")
    private let sFrontK = NSTextField(labelWithString: "")
    private let sFrontV = NSTextField(labelWithString: "auto")
    private let sSocksK = NSTextField(labelWithString: "SOCKS5")
    private let sSocksV = NSTextField(labelWithString: ":1080")
    private let sparkL = MarqueeLab(frame: .zero)
    private let sparkR = NSTextField(labelWithString: "")
    private let journalBtn = PillButton(frame: .zero)
    private let resetBtn = PillButton(frame: .zero)
    private let setBtn = PillButton(frame: .zero)
    private let footLine = FlippedView(frame: .zero)
    private let cards = [LoftPlate(), LoftPlate(), LoftPlate()]

    /// Builds the status window around `engine` and reloads on `.loftState`.
    init(engine: SessionController) {
        self.engine = engine
        let w = makeLoftWindow(size: NSSize(width: 400, height: 500))
        super.init(window: w)
        w.level = .normal
        build()
        w.center()
        NotificationCenter.default.addObserver(self, selector: #selector(reload), name: .loftState, object: nil)
        reload()
    }

    /// Storyboard path is unused.
    required init?(coder: NSCoder) { fatalError() }

    /// Lays out mark, pill, action, cards, spark, and footer buttons.
    private func build() {
        let c = loftBody(window!)
        titleLab.font = Palette.font(22)
        titleLab.textColor = Palette.snow
        titleLab.alignment = .center
        Palette.fitLine(titleLab)
        pillBox.wantsLayer = true
        pillBox.layer?.backgroundColor = Palette.field.cgColor
        pillBox.layer?.cornerRadius = 12
        pillBox.layer?.masksToBounds = false
        pillLab.font = Palette.body(11.5)
        pillLab.textColor = Palette.mute
        pillLab.alignment = .left
        Palette.fitLine(pillLab)
        pillHit.drawsWash = false
        pillHit.target = NSApp.delegate
        pillHit.action = #selector(AppDelegate.showList)
        dot.wantsLayer = true
        dot.layer?.cornerRadius = 3.5
        pillBox.addSubview(dot)
        pillBox.addSubview(pillLab)
        pillBox.addSubview(pillHit)
        action.target = self
        action.action = #selector(tap)
        for (card, labs) in zip(cards, [[sOverK, sOverV], [sFrontK, sFrontV], [sSocksK, sSocksV]]) {
            card.fill = Palette.field
            card.radius = 10
            for l in labs { card.addSubview(l) }
            c.addSubview(card)
        }
        for k in [sOverK, sFrontK, sSocksK] {
            k.font = Palette.body(10.5)
            k.textColor = Palette.mute
            Palette.fitLine(k)
        }
        for v in [sOverV, sFrontV, sSocksV] {
            v.font = Palette.mono(12)
            v.textColor = Palette.snow
            Palette.fitLine(v)
        }
        sparkL.font = Palette.body(11.5)
        sparkL.textColor = Palette.mute
        sparkR.font = Palette.body(11.5)
        sparkR.textColor = Palette.mute
        sparkR.alignment = .right
        Palette.fitLine(sparkR)
        footLine.wantsLayer = true
        footLine.layer?.backgroundColor = Palette.line.cgColor
        journalBtn.compact = true
        resetBtn.compact = true
        setBtn.compact = true
        journalBtn.target = NSApp.delegate
        journalBtn.action = #selector(AppDelegate.showJournal)
        resetBtn.target = self
        resetBtn.action = #selector(resetTap)
        setBtn.target = NSApp.delegate
        setBtn.action = #selector(AppDelegate.showSettings)
        c.addSubview(mark)
        c.addSubview(titleLab)
        c.addSubview(pillBox)
        c.addSubview(action)
        c.addSubview(sparkL)
        c.addSubview(sparkR)
        c.addSubview(footLine)
        c.addSubview(journalBtn)
        c.addSubview(resetBtn)
        c.addSubview(setBtn)
    }

    /// Relays session state into captions, cards, and spark.
    @objc func reload() {
        let e = engine
        let p = e.store.active()
        let on = e.state == .on
        let fail = e.state == .error
        let held = e.holdArmed
        let connecting = e.state == .connecting
        let disconnecting = e.state == .disconnecting
        let busy = connecting || disconnecting
        mark.state = (held || fail) ? .err : (on ? .on : .off)
        let pillText: String
        if on {
            let name: String
            if let p = p, !p.name.isEmpty { name = p.name }
            else if let p = p { name = Palette.hostOf(p.url) }
            else { name = L10n.t("pill_on") }
            let time = e.connectedAt.map { Palette.uptime($0) } ?? "0:00"
            pillText = "\(L10n.t("pill_on")): \(name) \(time)"
        } else {
            pillText = L10n.t("pill_off")
        }
        pillLab.stringValue = pillText
        dot.layer?.backgroundColor = ((held || fail) ? Palette.err : (on ? Palette.lamp : Palette.mute)).cgColor
        action.lampOn = on && !held && !busy
        action.isEnabled = !disconnecting
        action.title = e.actionTitle()
        setKicker(sOverK, "OVERLAY")
        setKicker(sFrontK, L10n.t("front").uppercased())
        setKicker(sSocksK, "SOCKS5")
        let simple = e.store.simple
        for card in cards { card.isHidden = simple }
        sparkL.isHidden = simple
        sparkR.isHidden = simple
        journalBtn.title = L10n.t("journal")
        resetBtn.title = L10n.t("reset")
        setBtn.title = L10n.t("settings")
        if on {
            sOverV.stringValue = e.overlayIP.isEmpty ? "—" : e.overlayIP
            sFrontV.stringValue = e.frontName.isEmpty ? (p?.kind ?? "get") : e.frontName
        } else {
            sOverV.stringValue = "—"
            sFrontV.stringValue = "auto"
        }
        let spark = e.sparkLine()
        sparkL.textColor = spark.color
        if spark.pair {
            sparkR.stringValue = "↓ \(Palette.rate(e.downRate, bits: e.store.rateBits))"
            sparkR.textColor = Palette.mute
        } else {
            sparkR.stringValue = ""
        }
        let socksShown = on ? e.socksDisplay : ""
        if socksShown.isEmpty {
            sSocksV.stringValue = "—"
        } else {
            sSocksV.stringValue = ":" + Palette.socksPort(socksShown)
        }
        layout()
        let crawl = connecting || disconnecting
        sparkL.show(spark.text, scroll: spark.scroll, keepMoving: crawl)
    }

    /// Positions status chrome; simple view hides cards and spark.
    private func layout() {
        let c = loftBody(window!)
        let w = c.bounds.width > 1 ? c.bounds.width : 400
        let simple = engine.store.simple
        let wideSpark = engine.state == .error || engine.state == .connecting || engine.state == .disconnecting
        mark.frame = NSRect(x: round((w - 72) / 2), y: 16, width: 72, height: 72)
        titleLab.frame = NSRect(x: 22, y: 92, width: w - 44, height: 28)
        let pillNeed = Palette.textWidth(pillLab.stringValue, font: Palette.body(11.5)) + 36
        let pillW = min(w - 44, max(128, pillNeed))
        pillBox.frame = NSRect(x: round((w - pillW) / 2), y: 126, width: pillW, height: 24)
        dot.frame = NSRect(x: 10, y: 8.5, width: 7, height: 7)
        pillLab.frame = NSRect(x: 22, y: 3, width: pillW - 32, height: 18)
        pillHit.frame = pillBox.bounds
        action.frame = NSRect(x: 22, y: 166, width: w - 44, height: 42)
        if !simple {
            let gap: CGFloat = 8
            let parts = loftSplit(w - 44, count: 3, gap: gap)
            let pairs = [(sOverK, sOverV), (sFrontK, sFrontV), (sSocksK, sSocksV)]
            var cx: CGFloat = 22
            for (i, card) in cards.enumerated() {
                let cw = parts[i]
                card.frame = NSRect(x: cx, y: 226, width: cw, height: 58)
                pairs[i].0.frame = NSRect(x: 10, y: 8, width: cw - 20, height: 18)
                pairs[i].1.frame = NSRect(x: 10, y: 30, width: cw - 20, height: 20)
                cx += cw + gap
            }
            sparkL.frame = NSRect(x: 22, y: 300, width: wideSpark ? w - 44 : 160, height: 18)
            sparkR.frame = NSRect(x: w - 22 - 160, y: 300, width: 160, height: 18)
            sparkR.isHidden = wideSpark
        }
        let bodyH: CGFloat = simple ? 310 : 490
        footLine.frame = NSRect(x: 0, y: bodyH - 72, width: w, height: 1)
        let gap: CGFloat = 8
        let inner = w - 40
        let font = Palette.font(11.5)
        /// Minimum footer-button width for title `s`.
        func need(_ s: String) -> CGFloat { max(72, Palette.textWidth(s, font: font) + 14) }
        var jW = need(journalBtn.title)
        var rW = need(resetBtn.title)
        var sW = need(setBtn.title)
        let extra = inner - (jW + rW + sW + gap * 2)
        if extra >= 0 {
            let extraL = floor(extra / 2)
            jW += extraL
            sW += extra - extraL
        } else {
            let sides = jW + sW
            if sides > -extra {
                jW += extra * (jW / sides)
                sW += extra * (sW / sides)
            } else {
                jW = 72
                sW = 72
                rW = max(72, inner - jW - sW - gap * 2)
            }
        }
        jW = round(jW)
        rW = round(rW)
        sW = round(sW)
        let by = bodyH - 58
        journalBtn.frame = NSRect(x: 20, y: by, width: jW, height: 36)
        resetBtn.frame = NSRect(x: 20 + jW + gap, y: by, width: rW, height: 36)
        setBtn.frame = NSRect(x: 20 + jW + gap + rW + gap, y: by, width: sW, height: 36)
        if let win = window {
            loftResize(win, bodyHeight: bodyH)
        }
    }

    /// Uppercase kicker with a little tracking.
    private func setKicker(_ field: NSTextField, _ text: String) {
        let p = NSMutableParagraphStyle()
        p.lineBreakMode = .byTruncatingTail
        field.attributedStringValue = NSAttributedString(string: text, attributes: [
            .font: Palette.body(10.5),
            .foregroundColor: Palette.mute,
            .kern: 0.45,
            .paragraphStyle: p,
        ])
    }

    /// Restores pre-loft routes.
    @objc private func resetTap() { engine.resetRoutes() }

    /// Connects, or opens onboard if there is no profile.
    @objc private func tap() {
        LoftLog.shared.tap("status", action.title)
        if engine.store.profiles.isEmpty {
            AppDelegate.shared?.showOnboard()
            return
        }
        engine.toggle()
    }
}

/// Connections list: profiles, multi toggle, import/export, drop target.
final class ConnectionsWindowController: NSWindowController {
    private let engine: SessionController
    private let titleLab = NSTextField(labelWithString: "")
    private let addLab = NSTextField(labelWithString: "+")
    private let addBtn = ActionButton(frame: .zero)
    private let multiCap = NSTextField(labelWithString: "")
    private let multiSw = ToggleSwitch(frame: .zero)
    private let sub = NSTextField(labelWithString: "")
    private let list = FlippedView(frame: .zero)
    private let imp = PillButton(frame: .zero)
    private let exp = PillButton(frame: .zero)
    private let set = PillButton(frame: .zero)
    private let footLine = FlippedView(frame: .zero)
    private let dropPlate = LoftPlate(frame: .zero)
    private let drop = NSTextField(wrappingLabelWithString: "")

    /// Builds the connections window around `engine` and rebuilds on `.loftState`.
    init(engine: SessionController) {
        self.engine = engine
        let w = makeLoftWindow(size: NSSize(width: 400, height: 520))
        super.init(window: w)
        w.level = .normal
        build()
        w.center()
        NotificationCenter.default.addObserver(self, selector: #selector(rebuild), name: .loftState, object: nil)
        rebuild()
    }

    /// Storyboard path is unused.
    required init?(coder: NSCoder) { fatalError() }

    /// Lays out title, multi toggle, drop plate, and footer buttons.
    private func build() {
        let c = loftBody(window!)
        titleLab.font = Palette.font(20)
        titleLab.textColor = Palette.snow
        titleLab.usesSingleLineMode = true
        titleLab.lineBreakMode = .byTruncatingTail
        titleLab.cell?.wraps = false
        titleLab.cell?.truncatesLastVisibleLine = true
        addLab.font = Palette.font(20)
        addLab.textColor = Palette.mute
        addLab.alignment = .center
        addLab.usesSingleLineMode = true
        addBtn.target = self
        addBtn.action = #selector(manual)
        multiCap.font = Palette.body(12.5)
        multiCap.textColor = Palette.mute
        multiCap.alignment = .right
        multiCap.usesSingleLineMode = true
        multiCap.lineBreakMode = .byTruncatingTail
        multiCap.cell?.wraps = false
        multiCap.cell?.truncatesLastVisibleLine = true
        multiSw.onChange = { [weak self] on in
            self?.engine.setMulti(on)
            self?.rebuild()
        }
        sub.font = Palette.body(12.5)
        sub.textColor = Palette.mute
        drop.font = Palette.body(13)
        drop.textColor = Palette.mute
        drop.alignment = .center
        footLine.wantsLayer = true
        footLine.layer?.backgroundColor = Palette.line.cgColor
        imp.compact = true
        exp.compact = true
        set.compact = true
        imp.target = self; imp.action = #selector(impTap)
        exp.target = self; exp.action = #selector(expTap)
        set.target = self; set.action = #selector(setTap)
        dropPlate.radius = 12
        dropPlate.stroke = Palette.slate
        addBtn.washRadius = 6
        for v in [titleLab, addLab, addBtn, multiCap, multiSw, sub, list, dropPlate, drop, footLine, imp, exp, set] as [NSView] {
            c.addSubview(v)
        }
        c.registerForDraggedTypes([.fileURL])
    }

    /// Rebuilds the profile rows from `engine.store`.
    @objc func rebuild() {
        let c = loftBody(window!)
        let w = c.bounds.width > 1 ? c.bounds.width : 400
        titleLab.stringValue = L10n.t("conns")
        multiCap.stringValue = L10n.t("several")
        multiSw.on = engine.store.multi
        sub.stringValue = engine.store.multi ? L10n.t("conns_sub_multi") : L10n.t("conns_sub")
        imp.title = L10n.t("import")
        exp.title = L10n.t("export")
        set.title = L10n.t("settings")
        drop.stringValue = L10n.t("drop")
        let swW: CGFloat = 36
        let swX = w - 20 - swW
        multiSw.frame = NSRect(x: swX, y: 12, width: swW, height: 22)
        let capNeed = Palette.textWidth(multiCap.stringValue, font: Palette.body(12.5))
        let plusW: CGFloat = 22
        let titleNeed = Palette.textWidth(titleLab.stringValue, font: Palette.font(20))
        // Right cluster (caption + toggle) keeps its natural width; title yields if the row is tight.
        let capW = min(capNeed, max(24, swX - 8 - (20 + 80 + 4 + plusW + 12)))
        multiCap.frame = NSRect(x: swX - 8 - capW, y: 13, width: capW, height: 18)
        let titleMax = multiCap.frame.minX - 12 - 20 - 4 - plusW
        let titleW = min(titleNeed, max(80, titleMax))
        titleLab.frame = NSRect(x: 20, y: 8, width: titleW, height: 28)
        addLab.frame = NSRect(x: 20 + titleW + 2, y: 8, width: plusW, height: 28)
        addBtn.frame = addLab.frame
        sub.lineBreakMode = .byTruncatingTail
        sub.usesSingleLineMode = true
        sub.frame = NSRect(x: 20, y: 40, width: w - 40, height: 18)
        list.subviews.forEach { $0.removeFromSuperview() }
        var y: CGFloat = 0
        let simple = engine.store.simple
        let multi = engine.store.multi
        let busy = !multi && (engine.state == .connecting || engine.state == .disconnecting)
        let actFont = Palette.font(11.5)
        let actTitles = [L10n.t("pop_connect"), L10n.t("disconnect"), L10n.t("connecting"), L10n.t("disconnecting"), L10n.t("retry"), L10n.t("cancel"), String(format: L10n.t("retry_wait"), 15)]
        let actW = max(72, (actTitles.map { Palette.textWidth($0, font: actFont) }.max() ?? 72) + 18)
        let actH: CGFloat = 26
        if engine.store.profiles.isEmpty {
            drop.isHidden = false
            dropPlate.isHidden = false
            drop.frame = NSRect(x: 20, y: 80, width: w - 40, height: 80)
            dropPlate.frame = drop.frame
        } else {
            drop.isHidden = true
            dropPlate.isHidden = true
        }
        for p in engine.store.profiles {
            let rowH: CGFloat = simple ? 44 : 52
            let rowW = w - 40
            let row = LoftPlate(frame: NSRect(x: 10, y: y, width: rowW, height: rowH))
            row.radius = 10
            let active = p.id == engine.store.activeID
            let st = engine.state(of: p.id)
            let live = st == .on
            if active || live {
                row.fill = Palette.field
                row.stroke = Palette.slate
            }
            let act = PillButton(frame: .zero)
            act.compact = true
            act.identifier = NSUserInterfaceItemIdentifier(p.id)
            act.target = self
            act.action = #selector(toggleOne(_:))
            if st == .error {
                act.title = engine.actionTitle(of: p.id)
                act.lampOn = false
                act.isEnabled = true
            } else if st == .disconnecting {
                act.title = L10n.t("disconnecting")
                act.lampOn = false
                act.isEnabled = false
            } else if st == .connecting {
                act.title = engine.actionTitle(of: p.id)
                act.lampOn = false
                act.isEnabled = true
            } else if live {
                act.title = L10n.t("disconnect")
                act.lampOn = true
                act.isEnabled = true
            } else {
                act.title = L10n.t("pop_connect")
                act.lampOn = false
                act.isEnabled = !busy
            }
            let actX: CGFloat = 8
            act.frame = NSRect(x: actX, y: round((rowH - actH) / 2), width: actW, height: actH)
            let textX = actX + actW + 8
            let sideStr: String
            if multi {
                if live, !engine.overlayIP(of: p.id).isEmpty { sideStr = engine.overlayIP(of: p.id) }
                else if st == .error { sideStr = L10n.t("fail") }
                else if live { sideStr = L10n.t("conn_up") }
                else { sideStr = p.kind }
            } else {
                sideStr = active ? L10n.t("active") : p.kind
            }
            let editStr = L10n.t("edit")
            let sideW = max(
                52,
                Palette.textWidth(sideStr, font: Palette.body(10.5)),
                Palette.textWidth(editStr, font: Palette.body(11.5))
            ) + 4
            let sideX = rowW - 8 - sideW
            let nameFont = Palette.font(13.5)
            let showTag = p.tun && engine.store.defaultRoute && !simple
            let tagStr = L10n.t("route_tag").uppercased()
            let tagFont = Palette.font(10)
            let tagW: CGFloat = showTag ? Palette.textWidth(tagStr, font: tagFont) : 0
            let nameMax = max(48, sideX - textX - 8 - (showTag ? tagW + 6 : 0))
            let nameW = min(Palette.textWidth(p.name, font: nameFont), nameMax)
            let n = Palette.label(p.name, font: nameFont, color: Palette.snow, frame: NSRect(x: textX, y: simple ? 12 : 8, width: nameW, height: 18))
            let hit = ActionButton(frame: row.bounds)
            hit.drawsWash = false
            hit.identifier = NSUserInterfaceItemIdentifier(p.id)
            hit.target = self
            hit.action = #selector(pick(_:))
            row.addSubview(hit)
            row.addSubview(act)
            row.addSubview(n)
            if showTag {
                let tag = Palette.label(tagStr, font: tagFont, color: Palette.lamp, frame: NSRect(x: textX + nameW + 6, y: 10, width: tagW, height: 14))
                row.addSubview(tag)
            }
            if !simple {
                var sub = Palette.hostOf(p.url)
                if p.socksOn {
                    let port = Palette.socksPort(engine.store.socksBind(for: p))
                    if !port.isEmpty { sub += " · " + port }
                }
                let u = Palette.label(sub, font: Palette.mono(11.5), color: Palette.mute, frame: NSRect(x: textX, y: 28, width: max(40, sideX - textX - 8), height: 14))
                row.addSubview(u)
            }
            let side = Palette.label(sideStr, font: Palette.body(10.5), color: (active || live) ? Palette.lamp : Palette.stone, frame: NSRect(x: sideX, y: simple ? 6 : 8, width: sideW, height: 14))
            side.alignment = .right
            row.addSubview(side)
            let ed = GhostButton(frame: NSRect(x: sideX, y: simple ? 22 : 26, width: sideW, height: 16))
            ed.title = L10n.t("edit")
            ed.font = Palette.body(11.5)
            ed.tone = Palette.mute
            ed.alignment = .right
            ed.identifier = NSUserInterfaceItemIdentifier(p.id)
            ed.target = self
            ed.action = #selector(editOne(_:))
            row.addSubview(ed)
            list.addSubview(row)
            y += row.frame.height + 4
        }
        list.frame = NSRect(x: 10, y: 66, width: w - 20, height: min(y, 340))
        footLine.frame = NSRect(x: 0, y: 430, width: w, height: 1)
        let parts = loftSplit(w - 40, count: 3, gap: 8)
        imp.frame = NSRect(x: 20, y: 444, width: parts[0], height: 36)
        exp.frame = NSRect(x: 20 + parts[0] + 8, y: 444, width: parts[1], height: 36)
        set.frame = NSRect(x: 20 + parts[0] + 8 + parts[1] + 8, y: 444, width: parts[2], height: 36)
    }

    /// Selects the profile on this row without connecting.
    @objc private func pick(_ sender: NSButton) {
        let id = sender.identifier?.rawValue ?? ""
        if id.isEmpty || id == engine.store.activeID { return }
        paintSelection(id)
        engine.selectProfile(id)
    }

    /// Connects or disconnects the profile on this row.
    @objc private func toggleOne(_ sender: NSButton) {
        LoftLog.shared.tap("list", sender.title)
        let id = sender.identifier?.rawValue ?? ""
        guard engine.store.profiles.contains(where: { $0.id == id }) else { return }
        engine.toggle(id)
    }

    /// Highlights the row whose hit target is `id`.
    private func paintSelection(_ id: String) {
        for row in list.subviews.compactMap({ $0 as? LoftPlate }) {
            let hit = row.subviews.compactMap({ $0 as? ActionButton }).first
            let on = hit?.identifier?.rawValue == id
            let live = on && engine.state(of: id) == .on
            row.fill = (on || live) ? Palette.field : nil
            row.stroke = (on || live) ? Palette.slate : nil
        }
    }

    /// Opens the editor for the profile on this row.
    @objc private func editOne(_ sender: NSButton) {
        let id = sender.identifier?.rawValue ?? ""
        guard let p = engine.store.profiles.first(where: { $0.id == id }) else { return }
        AppDelegate.shared?.showEdit(p)
    }

    /// Open-panel import of `.loft` / YAML into the store.
    @objc private func impTap() {
        let p = NSOpenPanel()
        p.allowedFileTypes = ["loft", "yaml", "yml", "vpn"]
        p.begin { [weak self] r in
            if r == NSApplication.ModalResponse.OK, let u = p.url {
                _ = self?.engine.store.importFile(u)
                self?.rebuild()
            }
        }
    }

    /// Save-panel export of the active profile (warns that the key is in the file).
    @objc private func expTap() {
        guard let prof = engine.store.active() else { return }
        let alert = NSAlert()
        alert.messageText = L10n.t("export")
        alert.informativeText = L10n.t("warn_export")
        alert.addButton(withTitle: L10n.t("export"))
        alert.addButton(withTitle: L10n.t("back"))
        if alert.runModal() != .alertFirstButtonReturn { return }
        let p = NSSavePanel()
        p.allowedFileTypes = ["loft"]
        p.nameFieldStringValue = "\(prof.name).loft"
        p.begin { [weak self] r in
            if r == NSApplication.ModalResponse.OK, let u = p.url {
                try? self?.engine.store.exportYAML(prof).write(to: u, atomically: true, encoding: .utf8)
            }
        }
    }

    /// Opens settings from the list footer.
    @objc private func setTap() { AppDelegate.shared?.showSettings() }

    /// Opens the editor with a blank GET profile.
    @objc private func manual() {
        let p = LoftProfile(id: "", name: "", token: "", url: "", kind: "get", path: "/static/js", socks: "auto", tun: true, mux: true, instanceID: "", file: "")
        AppDelegate.shared?.showEdit(p)
    }
}

/// Settings window: general, route, and client tabs of prefs.
final class SettingsWindowController: NSWindowController {
    private let engine: SessionController
    private var switches: [String: ToggleSwitch] = [:]
    private var tab = 0
    private var socksPortF: LoftField?

    /// Builds the settings window around `engine`.
    init(engine: SessionController) {
        self.engine = engine
        let w = makeLoftWindow(size: NSSize(width: 440, height: 420))
        super.init(window: w)
        w.level = .normal
        w.center()
        reload()
    }

    /// Storyboard path is unused.
    required init?(coder: NSCoder) { fatalError() }

    /// Rebuilds the current tab from prefs.
    func reload() {
        commitSocksPort()
        socksPortF = nil
        let c = loftBody(window!)
        c.subviews.forEach { $0.removeFromSuperview() }
        switches.removeAll()
        let resetTitle = L10n.t("reset")
        let resetW = min(200, Palette.textWidth(resetTitle, font: Palette.font(11.5)) + 22)
        let headY: CGFloat = 16
        let headH: CGFloat = 28
        let title = DrawLab(frame: NSRect(x: 22, y: headY, width: 396 - resetW - 12, height: headH))
        title.text = L10n.t("settings")
        title.font = Palette.font(20)
        title.color = Palette.snow
        c.addSubview(title)
        let resetHead = PillButton(frame: NSRect(x: 22 + 396 - resetW, y: headY, width: resetW, height: headH))
        resetHead.compact = true
        resetHead.title = resetTitle
        resetHead.target = self
        resetHead.action = #selector(resetTap)
        c.addSubview(resetHead)
        var y: CGFloat = headY + headH + 12
        let tabs = SegControl(frame: NSRect(x: 22, y: y, width: 396, height: 32), titles: [L10n.t("set_gen"), L10n.t("route"), L10n.t("set_client")])
        tabs.value = tab
        tabs.onChange = { [weak self] v in
            self?.tab = v
            DispatchQueue.main.async { self?.reload() }
        }
        c.addSubview(tabs)
        y += 44
        let textW: CGFloat = 328
        let swX: CGFloat = 382
        /// One named pref row with a toggle.
        func pref(_ key: String, _ dkey: String, _ on: Bool) {
            let padT: CGFloat = 10
            let padB: CGFloat = 10
            let nameH: CGFloat = 20
            let n = Palette.label(L10n.t(key), font: Palette.body(13.5), color: Palette.snow, frame: NSRect(x: 22, y: y + padT, width: textW, height: nameH))
            let dStr = L10n.t(dkey)
            let dFont = Palette.body(11.5)
            let dH = max(16, min(32, Palette.textHeight(dStr, font: dFont, width: textW)))
            let d = Palette.wrap(dStr, font: dFont, color: Palette.mute, frame: NSRect(x: 22, y: y + padT + nameH, width: textW, height: dH), lines: 2)
            let rowH = padT + nameH + dH + padB
            let sw = ToggleSwitch(frame: NSRect(x: swX, y: y + round((rowH - 22) / 2), width: 36, height: 22))
            sw.on = on
            sw.onChange = { [weak self] v in self?.set(key, v) }
            switches[key] = sw
            let hit = ActionButton(frame: NSRect(x: 22, y: y, width: textW, height: rowH - 1))
            hit.drawsWash = false
            hit.identifier = NSUserInterfaceItemIdentifier(key)
            hit.target = self
            hit.action = #selector(prefHit(_:))
            let line = FlippedView(frame: NSRect(x: 22, y: y + rowH - 1, width: 396, height: 1))
            line.wantsLayer = true
            line.layer?.backgroundColor = Palette.line.cgColor
            c.addSubview(hit); c.addSubview(n); c.addSubview(d); c.addSubview(sw); c.addSubview(line)
            y += rowH
        }
        /// Bit/s vs B/s: two small pills stacked on the right.
        func rateRow() {
            let padT: CGFloat = 10
            let padB: CGFloat = 10
            let nameH: CGFloat = 20
            let bitW: CGFloat = 72
            let bitH: CGFloat = 22
            let bitGap: CGFloat = 4
            let stackH = bitH * 2 + bitGap
            let textW2: CGFloat = 396 - bitW - 12
            let n = Palette.label(L10n.t("rate_unit"), font: Palette.body(13.5), color: Palette.snow, frame: NSRect(x: 22, y: y + padT, width: textW2, height: nameH))
            let dStr = L10n.t("rate_unit_d")
            let dFont = Palette.body(11.5)
            let dH = max(16, min(32, Palette.textHeight(dStr, font: dFont, width: textW2)))
            let d = Palette.wrap(dStr, font: dFont, color: Palette.mute, frame: NSRect(x: 22, y: y + padT + nameH, width: textW2, height: dH), lines: 2)
            let rowH = max(padT + nameH + dH + padB, padT + stackH + padB)
            let stackY = y + round((rowH - stackH) / 2)
            let rateSeg = SegControl(
                frame: NSRect(x: 22 + 396 - bitW, y: stackY, width: bitW, height: stackH),
                titles: [L10n.t("u_kbit"), L10n.t("u_kb")],
                vertical: true,
                compact: true
            )
            rateSeg.value = engine.store.rateBits ? 0 : 1
            rateSeg.onChange = { [weak self] v in
                self?.engine.store.rateBits = v == 0
                self?.engine.store.savePrefs()
                NotificationCenter.default.post(name: .loftState, object: self?.engine)
            }
            let line = FlippedView(frame: NSRect(x: 22, y: y + rowH - 1, width: 396, height: 1))
            line.wantsLayer = true
            line.layer?.backgroundColor = Palette.line.cgColor
            c.addSubview(n); c.addSubview(d); c.addSubview(rateSeg); c.addSubview(line)
            y += rowH
        }
        /// Always / window / never as a dropdown on the right.
        func dockRow() {
            let padT: CGFloat = 10
            let padB: CGFloat = 10
            let nameH: CGFloat = 20
            let titles = [L10n.t("dock_always"), L10n.t("dock_window"), L10n.t("dock_never")]
            let face = Palette.body(12.5)
            let selW = min(200, max(148, (titles.map { Palette.textWidth($0, font: face) }.max() ?? 148) + 36))
            let selH: CGFloat = 32
            let textW2: CGFloat = 396 - selW - 12
            let n = Palette.label(L10n.t("dock"), font: Palette.body(13.5), color: Palette.snow, frame: NSRect(x: 22, y: y + padT, width: textW2, height: nameH))
            let dStr = L10n.t("dock_d")
            let dFont = Palette.body(11.5)
            let dH = max(16, min(32, Palette.textHeight(dStr, font: dFont, width: textW2)))
            let d = Palette.wrap(dStr, font: dFont, color: Palette.mute, frame: NSRect(x: 22, y: y + padT + nameH, width: textW2, height: dH), lines: 2)
            let rowH = max(padT + nameH + dH + padB, padT + selH + padB)
            let modes: [ProfileStore.DockMode] = [.always, .window, .never]
            let sel = LoftSelect(frame: NSRect(x: 22 + 396 - selW, y: y + round((rowH - selH) / 2), width: selW, height: selH))
            sel.titles = titles
            sel.value = modes.firstIndex(of: engine.store.dock) ?? 1
            sel.onChange = { [weak self] v in
                guard v >= 0, v < modes.count else { return }
                self?.engine.store.dock = modes[v]
                self?.engine.store.savePrefs()
                AppDelegate.shared?.syncDock()
            }
            let line = FlippedView(frame: NSRect(x: 22, y: y + rowH - 1, width: 396, height: 1))
            line.wantsLayer = true
            line.layer?.backgroundColor = Palette.line.cgColor
            c.addSubview(n); c.addSubview(d); c.addSubview(sel); c.addSubview(line)
            y += rowH
        }
        /// RU/EN as a dropdown on the right, same chrome as Dock.
        func langRow() {
            let padT: CGFloat = 10
            let padB: CGFloat = 10
            let nameH: CGFloat = 20
            let titles = [L10n.t("lang_ru"), L10n.t("lang_en")]
            let face = Palette.body(12.5)
            let selW = min(200, max(148, (titles.map { Palette.textWidth($0, font: face) }.max() ?? 148) + 36))
            let selH: CGFloat = 32
            let textW2: CGFloat = 396 - selW - 12
            let n = Palette.label(L10n.t("lang"), font: Palette.body(13.5), color: Palette.snow, frame: NSRect(x: 22, y: y + padT, width: textW2, height: nameH))
            let dStr = L10n.t("lang_d")
            let dFont = Palette.body(11.5)
            let dH = max(16, min(32, Palette.textHeight(dStr, font: dFont, width: textW2)))
            let d = Palette.wrap(dStr, font: dFont, color: Palette.mute, frame: NSRect(x: 22, y: y + padT + nameH, width: textW2, height: dH), lines: 2)
            let rowH = max(padT + nameH + dH + padB, padT + selH + padB)
            let sel = LoftSelect(frame: NSRect(x: 22 + 396 - selW, y: y + round((rowH - selH) / 2), width: selW, height: selH))
            sel.titles = titles
            sel.value = L10n.ru ? 0 : 1
            sel.onChange = { [weak self] v in
                guard v == 0 || v == 1 else { return }
                self?.engine.store.lang = v == 0 ? "ru" : "en"
                L10n.ru = v == 0
                self?.engine.store.savePrefs()
                self?.reload()
                NotificationCenter.default.post(name: .loftState, object: self?.engine)
            }
            let line = FlippedView(frame: NSRect(x: 22, y: y + rowH - 1, width: 396, height: 1))
            line.wantsLayer = true
            line.layer?.backgroundColor = Palette.line.cgColor
            c.addSubview(n); c.addSubview(d); c.addSubview(sel); c.addSubview(line)
            y += rowH
        }
        /// SOCKS5 port field on the client tab.
        func socksPortRow() {
            let padT: CGFloat = 10
            let padB: CGFloat = 10
            let nameH: CGFloat = 20
            let fieldW: CGFloat = 80
            let fieldH: CGFloat = 32
            let textW2: CGFloat = 396 - fieldW - 12
            let n = Palette.label(L10n.t("socks_port"), font: Palette.body(13.5), color: Palette.snow, frame: NSRect(x: 22, y: y + padT, width: textW2, height: nameH))
            let dStr = L10n.t("socks_port_d")
            let dFont = Palette.body(11.5)
            let dH = max(16, min(32, Palette.textHeight(dStr, font: dFont, width: textW2)))
            let d = Palette.wrap(dStr, font: dFont, color: Palette.mute, frame: NSRect(x: 22, y: y + padT + nameH, width: textW2, height: dH), lines: 2)
            let rowH = padT + nameH + dH + padB
            let f = LoftField(frame: NSRect(x: 22 + 396 - fieldW, y: y + round((rowH - fieldH) / 2), width: fieldW, height: fieldH), placeholder: "1080")
            f.stringValue = "\(engine.store.socksPort)"
            f.field.alignment = .center
            f.field.target = self
            f.field.action = #selector(socksPortCommit)
            (f.field.cell as? NSTextFieldCell)?.sendsActionOnEndEditing = true
            socksPortF = f
            let line = FlippedView(frame: NSRect(x: 22, y: y + rowH - 1, width: 396, height: 1))
            line.wantsLayer = true
            line.layer?.backgroundColor = Palette.line.cgColor
            c.addSubview(n); c.addSubview(d); c.addSubview(f); c.addSubview(line)
            y += rowH
        }
        /// Defaults, Journal, and About under every tab.
        func linksRow() {
            let ghostFace = Palette.body(12.5)
            /// Width of a ghost title, at least 40pt.
            func ghostW(_ s: String) -> CGFloat {
                max(40, Palette.textWidth(s, font: ghostFace) + 16)
            }
            let items: [(String, AnyObject?, Selector)] = [
                (L10n.t("defaults"), self, #selector(defaultsTap)),
                (L10n.t("journal"), NSApp.delegate as AnyObject?, #selector(AppDelegate.showJournal)),
                (L10n.t("about"), NSApp.delegate as AnyObject?, #selector(AppDelegate.showAbout)),
            ]
            let widths = items.map { ghostW($0.0) }
            let gap: CGFloat = 20
            let total = widths.reduce(0, +) + gap * CGFloat(items.count - 1)
            var x = 22 + (396 - total) / 2
            for i in items.indices {
                let b = GhostButton(frame: NSRect(x: x, y: y, width: widths[i], height: 22))
                b.title = items[i].0
                b.font = ghostFace
                b.tone = Palette.mute
                b.alignment = .center
                b.target = items[i].1
                b.action = items[i].2
                c.addSubview(b)
                x += widths[i] + gap
            }
            y += 22
        }
        switch tab {
        case 1:
            pref("default_route", "default_route_d", engine.store.defaultRoute)
            pref("failover", "failover_d", engine.store.failover)
            pref("hold", "hold_d", engine.store.hold)
            pref("split", "split_d", engine.store.split)
        case 2:
            pref("auto", "auto_d", engine.store.autoConnect)
            pref("reconnect", "reconnect_d", engine.store.reconnect)
            pref("multi", "multi_d", engine.store.multi)
            pref("socks_share", "socks_share_d", engine.store.sharedSocks)
            socksPortRow()
            pref("diag", "diag_d", engine.store.diag)
        default:
            pref("login", "login_d", engine.store.login)
            dockRow()
            pref("simple", "simple_d", engine.store.simple)
            rateRow()
            langRow()
        }
        y += 10
        linksRow()
        loftResize(window!, bodyHeight: y + 20)
    }

    /// Flips the toggle for the pref whose identifier is on `sender`.
    @objc private func prefHit(_ sender: NSButton) {
        let key = sender.identifier?.rawValue ?? ""
        guard let sw = switches[key] else { return }
        sw.flip()
    }

    /// Writes `key` into the store and applies side effects (login, diag, default route).
    private func set(_ key: String, _ on: Bool) {
        switch key {
        case "simple": engine.store.simple = on
        case "multi": engine.setMulti(on)
        case "default_route":
            engine.store.defaultRoute = on
            engine.syncDefaultRoute()
        case "hold": engine.store.hold = on
        case "failover": engine.store.failover = on
        case "split": engine.store.split = on
        case "diag":
            engine.store.diag = on
            LoftLog.shared.diag = on
        case "socks_share": engine.store.sharedSocks = on
        case "login":
            engine.store.login = on
            AppDelegate.shared?.setLogin(on)
        case "auto": engine.store.autoConnect = on
        case "reconnect":
            engine.store.reconnect = on
            if !on { engine.cancelReconnect() }
        default: break
        }
        engine.store.savePrefs()
        NotificationCenter.default.post(name: .loftState, object: engine)
    }

    /// Reads the SOCKS port field into prefs.
    @objc private func socksPortCommit() { commitSocksPort() }

    /// Clamps and stores the Settings SOCKS port.
    private func commitSocksPort() {
        guard let f = socksPortF else { return }
        let raw = f.stringValue.trimmingCharacters(in: .whitespacesAndNewlines)
        if let n = Int(raw), n > 0 {
            let v = ProfileStore.clampPort(n)
            if v != engine.store.socksPort {
                engine.store.socksPort = v
                engine.store.savePrefs()
                NotificationCenter.default.post(name: .loftState, object: engine)
            }
        }
        f.stringValue = "\(engine.store.socksPort)"
    }



    /// Restores pre-loft routes.
    @objc private func resetTap() { engine.resetRoutes() }

    /// Restores factory prefs (not language, not profiles) and applies side effects.
    @objc private func defaultsTap() {
        commitSocksPort()
        let loginWas = engine.store.login
        engine.store.restoreDefaults()
        if loginWas { AppDelegate.shared?.setLogin(false) }
        LoftLog.shared.diag = engine.store.diag
        if !engine.store.reconnect { engine.cancelReconnect() }
        engine.syncDefaultRoute()
        AppDelegate.shared?.syncDock()
        reload()
        NotificationCenter.default.post(name: .loftState, object: engine)
    }
}

/// Profile editor: VPN type loft or SSTP, then type-specific fields.
final class EditWindowController: NSWindowController {
    private let engine: SessionController
    private var profile: LoftProfile
    private var nameF: LoftField!
    private var keyF: LoftField!
    private var userF: LoftField?
    private var urlF: LoftField!
    private var pathF: LoftField?
    private var sniF: LoftField?
    private var socksF: LoftField!
    private var vpnSeg: SegControl!
    private var kindSeg: SegControl?
    private var socksSw: ToggleSwitch!
    private var tunSw: ToggleSwitch!
    private var muxSw: ToggleSwitch?
    private var tlsSw: ToggleSwitch?
    private var papSw: ToggleSwitch?
    private var dnsF: LoftField?
    private var dnsReplaceSw: ToggleSwitch?
    private var form: FlippedView!

    /// Builds the editor around `profile`.
    init(engine: SessionController, profile: LoftProfile) {
        self.engine = engine
        self.profile = profile
        let w = makeLoftWindow(size: NSSize(width: 440, height: 620))
        super.init(window: w)
        w.level = .normal
        build()
        w.center()
    }

    /// Storyboard path is unused.
    required init?(coder: NSCoder) { fatalError() }

    /// Name, VPN type, then the form for loft or SSTP.
    private func build() {
        let c = loftBody(window!)
        c.subviews.forEach { $0.removeFromSuperview() }
        let title = Palette.label(L10n.t("edit_title"), font: Palette.font(20), color: Palette.snow, frame: NSRect(x: 28, y: 6, width: 384, height: 26))
        title.alignment = .center
        c.addSubview(title)
        var y: CGFloat = 40
        let nl = Palette.label(L10n.t("name"), font: Palette.body(11.5), color: Palette.mute, frame: NSRect(x: 28, y: y, width: 384, height: 16))
        c.addSubview(nl)
        y += 18
        nameF = LoftField(frame: NSRect(x: 28, y: y, width: 384, height: 36), placeholder: "")
        nameF.stringValue = profile.name
        c.addSubview(nameF)
        y += 46
        let types = [L10n.t("vpn_loft"), L10n.t("vpn_sstp")]
        let typeFace = Palette.body(12)
        let cell = max(56, (types.map { Palette.textWidth($0, font: typeFace) }.max() ?? 56) + 22)
        let segW = min(260, cell * CGFloat(types.count) + 4)
        let rowH: CGFloat = 28
        let tl = Palette.label(L10n.t("vpn_type"), font: Palette.body(13.5), color: Palette.snow, frame: NSRect(x: 28, y: y + round((rowH - 18) / 2), width: 384 - segW - 12, height: 18))
        c.addSubview(tl)
        vpnSeg = SegControl(frame: NSRect(x: 28 + 384 - segW, y: y, width: segW, height: rowH), titles: types, capsule: true)
        vpnSeg.value = profile.isSSTP ? 1 : 0
        vpnSeg.onChange = { [weak self] v in self?.switchVPN(v == 1) }
        c.addSubview(vpnSeg)
        y += rowH + 16
        form = FlippedView(frame: NSRect(x: 0, y: y, width: 440, height: 400))
        c.addSubview(form)
        paintForm()
    }

    /// Reloads the type-specific block and resizes the window.
    private func paintForm() {
        form.subviews.forEach { $0.removeFromSuperview() }
        userF = nil; pathF = nil; kindSeg = nil; muxSw = nil; sniF = nil; tlsSw = nil; papSw = nil
        dnsF = nil; dnsReplaceSw = nil
        var y: CGFloat = 0
        /// Mute caption inside `form`.
        func lab(_ k: String) {
            let l = Palette.label(L10n.t(k), font: Palette.body(11.5), color: Palette.mute, frame: NSRect(x: 28, y: y, width: 384, height: 16))
            form.addSubview(l)
            y += 18
        }
        /// Field inside `form`.
        func field(_ ph: String, _ val: String, secure: Bool = false) -> LoftField {
            let f = LoftField(frame: NSRect(x: 28, y: y, width: 384, height: 36), secure: secure, placeholder: ph)
            f.stringValue = val
            form.addSubview(f)
            y += 42
            return f
        }
        let sstp = profile.isSSTP
        if sstp {
            lab("sstp_user")
            userF = field("", profile.user)
            lab("sstp_pass")
            keyF = field("", profile.token, secure: true)
            lab("sstp_url")
            urlF = field("https://", profile.url)
            lab("sstp_sni")
            sniF = field("", profile.sni)
        } else {
            lab("key")
            keyF = field("lt_", profile.token, secure: true)
            lab("url")
            urlF = field("https://", profile.url)
            lab("url_type")
            let seg = SegControl(frame: NSRect(x: 28, y: y, width: 384, height: 36), titles: LoftProfile.frontTitles, compact: true)
            seg.value = LoftProfile.frontIndex(profile.kind)
            form.addSubview(seg)
            kindSeg = seg
            y += 44
            let pathL = Palette.label(L10n.t("path"), font: Palette.body(11.5), color: Palette.mute, frame: NSRect(x: 28, y: y, width: 180, height: 16))
            form.addSubview(pathL)
            y += 18
            let pf = LoftField(frame: NSRect(x: 28, y: y, width: 384, height: 36), placeholder: "/static/v3/build")
            pf.stringValue = profile.path
            form.addSubview(pf)
            pathF = pf
            y += 42
        }
        let defPort = engine.store.socksPort
        let socksL = Palette.label("SOCKS5", font: Palette.body(11.5), color: Palette.mute, frame: NSRect(x: 28, y: y, width: 384, height: 16))
        form.addSubview(socksL)
        y += 18
        socksF = LoftField(frame: NSRect(x: 28, y: y, width: 384, height: 36), placeholder: "127.0.0.1:\(defPort)")
        socksF.stringValue = profile.socksOn ? profile.dedicatedSocksDisplay(defaultPort: defPort) : ""
        form.addSubview(socksF)
        y += 50
        let capW: CGFloat = 320
        func inbound(_ key: String, _ dkey: String, _ on: Bool) -> ToggleSwitch {
            let n = Palette.label(L10n.t(key), font: Palette.body(13.5), color: Palette.snow, frame: NSRect(x: 28, y: y, width: capW, height: 18))
            let dStr = L10n.t(dkey)
            let dFont = Palette.body(11.5)
            let dH = max(28, Palette.textHeight(dStr, font: dFont, width: capW))
            let d = Palette.wrap(dStr, font: dFont, color: Palette.mute, frame: NSRect(x: 28, y: y + 20, width: capW, height: dH), lines: 3)
            let sw = ToggleSwitch(frame: NSRect(x: 376, y: y + 2, width: 36, height: 22))
            sw.on = on
            form.addSubview(n); form.addSubview(d); form.addSubview(sw)
            y += 20 + dH + 14
            return sw
        }
        socksSw = inbound("socks", "socks_d", profile.socksOn)
        socksSw.onChange = { [weak self] on in self?.armSocks(on) }
        armSocks(profile.socksOn)
        tunSw = inbound(sstp ? "sstp_tun" : "tun", sstp ? "sstp_tun_d" : "tun_d", profile.tun)
        if sstp {
            tlsSw = inbound("sstp_tls", "sstp_tls_d", profile.tlsCheck)
            papSw = inbound("sstp_pap", "sstp_pap_d", profile.papCHAP)
            lab("sstp_dns")
            dnsF = field(L10n.t("sstp_dns_ph"), profile.dns)
            dnsReplaceSw = inbound("sstp_dns_replace", "sstp_dns_replace_d", profile.dnsReplace)
        } else {
            muxSw = inbound("mux", "mux_d", profile.mux)
        }
        let save = PillButton(frame: NSRect(x: 28, y: y, width: 384, height: 42))
        save.title = L10n.t("save")
        save.lampOn = true
        save.target = self
        save.action = #selector(saveTap)
        form.addSubview(save)
        y += 48
        let back = GhostButton(frame: NSRect(x: 28, y: y, width: 120, height: 22))
        back.title = L10n.t("back")
        back.font = Palette.body(12.5)
        back.tone = Palette.mute
        back.alignment = .left
        back.target = self
        back.action = #selector(backTap)
        form.addSubview(back)
        if !profile.id.isEmpty {
            let del = GhostButton(frame: NSRect(x: 292, y: y, width: 120, height: 22))
            del.title = L10n.t("delete")
            del.font = Palette.body(12.5)
            del.tone = Palette.copper
            del.alignment = .right
            del.target = self
            del.action = #selector(delTap)
            form.addSubview(del)
        }
        y += 28
        form.frame.size.height = y
        loftResize(window!, bodyHeight: form.frame.minY + y + 12)
    }

    /// Harvests fields, then shows loft or SSTP form.
    private func switchVPN(_ sstp: Bool) {
        harvest()
        if sstp {
            profile.kind = "sstp"
            profile.mux = false
            profile.tun = true
        } else if profile.isSSTP {
            profile.kind = "get"
        }
        paintForm()
    }

    /// Copies visible fields into `profile`.
    private func harvest() {
        profile.name = nameF.stringValue
        let tok = keyF.stringValue
        if !tok.isEmpty { profile.token = tok }
        if let u = userF { profile.user = u.stringValue }
        if let s = sniF { profile.sni = s.stringValue.trimmingCharacters(in: .whitespacesAndNewlines) }
        if let t = tlsSw { profile.tlsCheck = t.on }
        if let p = papSw { profile.papCHAP = p.on }
        if let d = dnsF { profile.dns = LoftProfile.normalizeDNS(d.stringValue) }
        if let r = dnsReplaceSw { profile.dnsReplace = r.on }
        profile.url = urlF.stringValue
        if let p = pathF { profile.path = p.stringValue }
        if socksSw.on {
            profile.socks = LoftProfile.normalizeSocks(socksF.stringValue, defaultPort: engine.store.socksPort)
        } else {
            profile.socks = ""
        }
        profile.tun = tunSw.on
        if let m = muxSw { profile.mux = m.on }
        if let k = kindSeg, !profile.isSSTP {
            profile.kind = LoftProfile.frontKind(k.value)
        }
    }

    /// Dims and locks the SOCKS field when SOCKS is off.
    private func armSocks(_ on: Bool) {
        socksF.alphaValue = on ? 1 : 0.4
        socksF.field.isEditable = on
        socksF.field.isSelectable = on
    }

    /// Writes the form into the store; forces TUN if both inbounds are off.
    @objc private func saveTap() {
        harvest()
        var p = profile
        if p.isSSTP {
            p.tun = true
            p.mux = false
        }
        if !p.tun && !p.socksOn { p.tun = true }
        if p.id.isEmpty {
            p.id = engine.store.slug(p.name)
            if p.id.isEmpty { p.id = UUID().uuidString }
        }
        if p.name.isEmpty { p.name = Palette.hostOf(p.url) }
        engine.store.saveEdited(p)
        engine.store.ensureActive()
        if engine.store.activeID.isEmpty { engine.store.select(p.id) }
        NotificationCenter.default.post(name: .loftState, object: engine)
        window?.close()
    }

    /// Removes this profile from the store and closes.
    @objc private func delTap() {
        engine.store.remove(profile.id)
        NotificationCenter.default.post(name: .loftState, object: engine)
        window?.close()
    }

    /// Closes without saving.
    @objc private func backTap() { window?.close() }
}

/// Two-step onboard: access key, then URL/kind/path, then connect.
final class OnboardWindowController: NSWindowController {
    private let engine: SessionController
    private var step = 1
    private var token = ""
    private var url = "https://"
    private var path = "/static/v3/build"
    private var kind = "get"
    private var field: LoftField!
    private var pathF: LoftField?
    private var seg: SegControl?

    /// Builds the onboard window around `engine` and paints step 1.
    init(engine: SessionController) {
        self.engine = engine
        let w = makeLoftWindow(size: NSSize(width: 440, height: 520))
        super.init(window: w)
        w.level = .normal
        paint()
        w.center()
    }

    /// Storyboard path is unused.
    required init?(coder: NSCoder) { fatalError() }

    /// Rebuilds step 1 (key) or step 2 (URL).
    private func paint() {
        let c = loftBody(window!)
        c.subviews.forEach { $0.removeFromSuperview() }
        let mark = MarkView(frame: NSRect(x: 188, y: 16, width: 64, height: 64))
        mark.state = .on
        c.addSubview(mark)
        let stepL = Palette.label(step == 1 ? L10n.t("key_step") : L10n.t("url_step"), font: Palette.font(11), color: Palette.mute, frame: NSRect(x: 36, y: 88, width: 368, height: 16))
        stepL.alignment = .center
        let title = Palette.label(step == 1 ? L10n.t("key_title") : L10n.t("url_title"), font: Palette.font(22), color: Palette.snow, frame: NSRect(x: 36, y: 108, width: 368, height: 28))
        title.alignment = .center
        let body = NSTextField(wrappingLabelWithString: step == 1 ? L10n.t("key_body") : L10n.t("url_body"))
        body.font = Palette.body(13.5)
        body.textColor = Palette.mute
        body.alignment = .center
        body.frame = NSRect(x: 36, y: 142, width: 368, height: 36)
        c.addSubview(stepL); c.addSubview(title); c.addSubview(body)

        var y: CGFloat = 190
        if step == 1 {
            field = LoftField(frame: NSRect(x: 36, y: y, width: 368, height: 40), placeholder: "lt_")
            field.stringValue = token
            c.addSubview(field)
            y += 48
            let hint = NSTextField(wrappingLabelWithString: L10n.t("key_hint"))
            hint.font = Palette.body(12)
            hint.textColor = Palette.mute
            hint.frame = NSRect(x: 36, y: y, width: 368, height: 32)
            c.addSubview(hint)
            y += 48
            let go = PillButton(frame: NSRect(x: 36, y: y, width: 368, height: 42))
            go.title = L10n.t("continue")
            go.lampOn = true
            go.target = self
            go.action = #selector(next)
            c.addSubview(go)
            y += 50
            let alt = GhostButton(frame: NSRect(x: 36, y: y, width: 368, height: 22))
            alt.title = L10n.t("key_orfile")
            alt.font = Palette.body(12.5)
            alt.tone = Palette.mute
            alt.alignment = .center
            alt.target = self
            alt.action = #selector(imp)
            c.addSubview(alt)
        } else {
            let ul = Palette.label(L10n.t("url"), font: Palette.body(11.5), color: Palette.mute, frame: NSRect(x: 36, y: y, width: 368, height: 16))
            c.addSubview(ul)
            y += 18
            field = LoftField(frame: NSRect(x: 36, y: y, width: 368, height: 40), placeholder: "https://")
            field.stringValue = url
            c.addSubview(field)
            y += 48
            let tl = Palette.label(L10n.t("url_type"), font: Palette.body(11.5), color: Palette.mute, frame: NSRect(x: 36, y: y, width: 368, height: 16))
            c.addSubview(tl)
            y += 18
            let s = SegControl(frame: NSRect(x: 36, y: y, width: 368, height: 36), titles: LoftProfile.frontTitles, compact: true)
            s.value = LoftProfile.frontIndex(kind)
            s.onChange = { [weak self] v in self?.kind = LoftProfile.frontKind(v) }
            seg = s
            c.addSubview(s)
            y += 44
            let pl = Palette.label(L10n.t("path"), font: Palette.body(11.5), color: Palette.mute, frame: NSRect(x: 36, y: y, width: 368, height: 16))
            c.addSubview(pl)
            y += 18
            let pf = LoftField(frame: NSRect(x: 36, y: y, width: 368, height: 40), placeholder: "/static/v3/build")
            pf.stringValue = path
            pathF = pf
            c.addSubview(pf)
            y += 48
            let hint = NSTextField(wrappingLabelWithString: L10n.t("url_hint"))
            hint.font = Palette.body(12)
            hint.textColor = Palette.mute
            hint.frame = NSRect(x: 36, y: y, width: 368, height: 36)
            c.addSubview(hint)
            y += 44
            let go = PillButton(frame: NSRect(x: 36, y: y, width: 368, height: 42))
            go.title = L10n.t("url_go")
            go.lampOn = true
            go.target = self
            go.action = #selector(finish)
            c.addSubview(go)
            y += 48
            let back = GhostButton(frame: NSRect(x: 36, y: y, width: 368, height: 22))
            back.title = L10n.t("back")
            back.font = Palette.body(12.5)
            back.tone = Palette.mute
            back.alignment = .center
            back.target = self
            back.action = #selector(prev)
            c.addSubview(back)
        }
    }

    /// Stores the key and advances to the URL step.
    @objc private func next() {
        token = field.stringValue.trimmingCharacters(in: .whitespaces)
        if token.isEmpty { return }
        step = 2
        paint()
    }

    /// Keeps URL/path and returns to the key step.
    @objc private func prev() {
        url = field.stringValue
        path = pathF?.stringValue ?? path
        step = 1
        paint()
    }

    /// Saves a new profile, selects it, and connects.
    @objc private func finish() {
        url = field.stringValue.trimmingCharacters(in: .whitespaces)
        path = pathF?.stringValue ?? path
        if url.isEmpty || url == "https://" { return }
        var p = LoftProfile(id: "", name: Palette.hostOf(url), token: token, url: url, kind: kind, path: path.isEmpty ? "/static/js" : path, socks: "auto", tun: true, mux: true, instanceID: "", file: "")
        p.id = engine.store.slug(p.name)
        if p.id.isEmpty { p.id = UUID().uuidString }
        engine.store.saveEdited(p)
        engine.store.select(p.id)
        NotificationCenter.default.post(name: .loftState, object: engine)
        window?.close()
        engine.connect()
    }

    /// Imports a `.loft` / YAML file instead of typing a key.
    @objc private func imp() {
        let p = NSOpenPanel()
        p.allowedFileTypes = ["loft", "yaml", "yml", "vpn"]
        p.begin { [weak self] r in
            if r == NSApplication.ModalResponse.OK, let u = p.url {
                if let prof = self?.engine.store.importFile(u) {
                    self?.engine.store.select(prof.id)
                    NotificationCenter.default.post(name: .loftState, object: self?.engine)
                    self?.window?.close()
                }
            }
        }
    }
}
