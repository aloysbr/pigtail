import Foundation

/// Which native page is up: D2 (no answer) or one of the D8 refusals.
public enum DaemonDownVariant: Equatable, Sendable {
    case daemonDown
    case httpOff
    case refused

    public init(title: String) {
        switch title {
        case ConnectionFlow.httpOffTitle: self = .httpOff
        case ConnectionFlow.refusedTitle: self = .refused
        default: self = .daemonDown
        }
    }
}

public enum DaemonDownModel {
    public static let reloadHint = "Recharger (⌘R) pour réessayer"

    /// The status line. D2 counts down like the web banner
    /// (web/src/components/OfflineBanner.tsx), the countdown alone: the page
    /// title already says « pigtaild ne répond pas ». The D8 pages say how to
    /// retry, with no countdown (D18), while the silent retry goes on.
    public static func status(variant: DaemonDownVariant, secondsLeft: Int, fetching: Bool) -> String {
        switch variant {
        case .daemonDown:
            let tail = fetching || secondsLeft <= 0 ? "Nouvel essai en cours" : "Nouvel essai dans \(secondsLeft) s"
            return tail
        case .httpOff, .refused:
            return reloadHint
        }
    }
}

/// The native page, loaded as HTML in the same web view. Tokens copied from
/// the daemon's « Lien expiré » page (internal/daemon/web.go
/// webExpiredHTML), light and dark. No script: the countdown is driven
/// from Swift through updateScript.
public enum DaemonDownPage {
    public static func html(title: String, status: String, detail: String?) -> String {
        let detailLine = detail.map { "<p class=\"detail\">\(escape($0))</p>\n" } ?? ""
        return """
        <!doctype html>
        <html lang="fr">
        <head>
        <meta charset="utf-8">
        <meta name="viewport" content="width=device-width, initial-scale=1">
        <title>\(escape(title))</title>
        <style>
        :root{color-scheme:light dark;--bg:#f5f5f7;--card:#ffffff;--border:#e3e3e8;--text:#1d1d1f;--text-2:#6e6e73;--code-bg:#ebebef;
        --font-sans:-apple-system,BlinkMacSystemFont,"SF Pro Text",Inter,system-ui,sans-serif;--font-mono:ui-monospace,"SF Mono",Menlo,monospace}
        @media (prefers-color-scheme: dark){:root{--bg:#1c1c1e;--card:#2c2c2e;--border:#38383a;--text:#f5f5f7;--text-2:#a1a1a6;--code-bg:#3a3a3c}}
        body{margin:0;min-height:100vh;display:grid;place-items:center;padding:0 16px;background:var(--bg);color:var(--text);font:13px/1.45 var(--font-sans);-webkit-font-smoothing:antialiased}
        main{max-width:420px;background:var(--card);border:1px solid var(--border);border-radius:10px;padding:24px}
        h1{margin:0 0 8px;font-size:17px;font-weight:600}
        p{margin:0 0 16px;color:var(--text-2)}
        p:last-child{margin-bottom:0}
        .detail{font-family:var(--font-mono);white-space:nowrap}
        #status{font-variant-numeric:tabular-nums}
        </style>
        </head>
        <body>
        <main>
        <h1>\(escape(title))</h1>
        \(detailLine)<p id="status" role="status">\(escape(status))</p>
        </main>
        </body>
        </html>

        """
    }

    /// JavaScript that sets the status line's text, the value passed as a
    /// JSON string literal (no HTML parsing, no way out of the literal).
    public static func updateScript(status: String) -> String {
        "document.getElementById('status').textContent = \(jsStringLiteral(status));"
    }

    static func escape(_ s: String) -> String {
        var out = ""
        out.reserveCapacity(s.count)
        for ch in s {
            switch ch {
            case "&": out += "&amp;"
            case "<": out += "&lt;"
            case ">": out += "&gt;"
            case "\"": out += "&quot;"
            case "'": out += "&#39;"
            default: out.append(ch)
            }
        }
        return out
    }

    static func jsStringLiteral(_ s: String) -> String {
        var out = "\""
        for u in s.unicodeScalars {
            switch u {
            case "\"": out += "\\\""
            case "\\": out += "\\\\"
            case "\n": out += "\\n"
            case "\r": out += "\\r"
            case "<": out += "\\u003C"
            case ">": out += "\\u003E"
            case "\u{2028}": out += "\\u2028"
            case "\u{2029}": out += "\\u2029"
            default:
                if u.value < 0x20 {
                    out += String(format: "\\u%04X", u.value)
                } else {
                    out.unicodeScalars.append(u)
                }
            }
        }
        return out + "\""
    }
}
