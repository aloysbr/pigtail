import os

/// The bar's unified-log channels (D28). One subsystem, one category per
/// area; read with `/usr/bin/log show --predicate 'subsystem == "sh.pigtail.app"'`
/// (`/usr/bin/log`: in zsh, `log` is a builtin). Every interpolated value is
/// written `privacy: .public` — NSLog's interpolated text came out `<private>`.
public enum BarLog {
    public static let subsystem = "sh.pigtail.app"
    public static let lid = Logger(subsystem: subsystem, category: "lid")
    public static let events = Logger(subsystem: subsystem, category: "events")
    public static let alerts = Logger(subsystem: subsystem, category: "alerts")
    public static let gates = Logger(subsystem: subsystem, category: "gates")
    public static let login = Logger(subsystem: subsystem, category: "login")
}
