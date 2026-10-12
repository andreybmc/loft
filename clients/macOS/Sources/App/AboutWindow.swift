// About window: mark, version, protocol blurb, and credit line.

import AppKit

/// About window: mark, version, protocol rows, blurb, and credit.
final class AboutWindowController: NSWindowController {
    /// Builds the bordered about window around `software` (bundle version).
    convenience init(software: String) {
        let w = makeLoftWindow(size: NSSize(width: 360, height: 360))
        w.level = .normal
        let c = loftBody(w)
        let mark = MarkView(frame: NSRect(x: 144, y: 8, width: 72, height: 72))
        mark.state = .on
        let title = Palette.label("loft", font: Palette.font(22), color: Palette.snow, frame: NSRect(x: 20, y: 84, width: 320, height: 28))
        title.alignment = .center
        let ver = Palette.label(software, font: Palette.body(12.5), color: Palette.mute, frame: NSRect(x: 20, y: 112, width: 320, height: 18))
        ver.alignment = .center

        let kick = Palette.label(L10n.t("protos"), font: Palette.body(11), color: Palette.mute, frame: NSRect(x: 20, y: 148, width: 320, height: 14))
        kick.alignment = .center
        c.addSubview(kick)

        let keyFont = Palette.body(12.5)
        let valFont = Palette.mono(12)
        let keys = ["LFTP/1.0", "SSTP"]
        let vals = [
            LoftProfile.frontKinds.joined(separator: " · ") + " · mask",
            "ppp · mschapv2 · pap",
        ]
        let pad: CGFloat = 16
        let specGap: CGFloat = 10
        let keyW = max(70, Palette.textWidth("LFTP/1.0", font: keyFont) + 2)
        let valW = 360 - pad * 2 - keyW - specGap
        let originX: CGFloat = pad
        for i in 0..<2 {
            let y = CGFloat(166 + i * 18)
            let k = Palette.label(keys[i], font: keyFont, color: Palette.mute, frame: NSRect(x: originX, y: y, width: keyW, height: 16))
            k.alignment = .right
            let v = Palette.label(vals[i], font: valFont, color: Palette.snow, frame: NSRect(x: originX + keyW + specGap, y: y, width: valW, height: 16))
            c.addSubview(k)
            c.addSubview(v)
        }

        let bodyH: CGFloat = 332
        let madeH: CGFloat = 20
        let madeY = bodyH - 14 - madeH
        let blurbH: CGFloat = 48
        let specBottom: CGFloat = 202
        let blurbY = specBottom + round((madeY - specBottom - blurbH) / 2)

        let blurb = NSTextField(wrappingLabelWithString: L10n.t("blurb"))
        blurb.font = Palette.body(13.5)
        blurb.textColor = Palette.stone
        blurb.alignment = .center
        blurb.frame = NSRect(x: 28, y: blurbY, width: 304, height: blurbH)

        let madeFont = Palette.body(12)
        let leftStr = "Made with love"
        let rightStr = "by A³"
        let heartW: CGFloat = 12
        let heartH: CGFloat = 10
        let gap: CGFloat = 4
        let leftW = Palette.textWidth(leftStr, font: madeFont)
        let rightW = Palette.textWidth(rightStr, font: madeFont)
        let rowW = leftW + gap + heartW + gap + rightW
        var x = round((360 - rowW) / 2)
        let left = Palette.label(leftStr, font: madeFont, color: Palette.mute, frame: NSRect(x: x, y: madeY, width: leftW, height: madeH))
        x += leftW + gap
        let heart = HeartView(frame: NSRect(x: x - 2, y: madeY + round((madeH - heartH) / 2), width: heartW, height: heartH))
        x += heartW + gap
        let right = Palette.label(rightStr, font: madeFont, color: Palette.mute, frame: NSRect(x: x - 1, y: madeY, width: rightW, height: madeH))

        for v in [mark, title, ver, blurb, left, heart, right] as [NSView] { c.addSubview(v) }
        w.center()
        self.init(window: w)
    }
}
