import AppKit
import UserNotifications

struct NotificationMessage: Decodable {
    let id: String
    let title: String
    let body: String
    let url: String
}

struct Request: Decodable {
    let action: String
    let message: NotificationMessage
}

func safeURL(_ value: String) -> URL? {
    guard value.utf8.count <= 4096,
          !value.contains("\\"),
          !value.unicodeScalars.contains(where: { CharacterSet.controlCharacters.contains($0) }),
          let parts = URLComponents(string: value),
          let host = parts.host, !host.isEmpty,
          parts.user == nil, parts.password == nil,
          let url = parts.url else { return nil }
    let local = ["localhost", "127.0.0.1", "::1", "[::1]"].contains(host.lowercased())
    guard parts.scheme == "https" || (parts.scheme == "http" && local) else { return nil }
    let secrets = Set(["token", "access_token", "refresh_token", "api_key", "password", "secret"])
    guard !(parts.queryItems ?? []).contains(where: { secrets.contains($0.name.lowercased()) }) else { return nil }
    return url
}

final class AppDelegate: NSObject, NSApplicationDelegate, UNUserNotificationCenterDelegate {
    func applicationWillFinishLaunching(_ notification: Notification) {
        UNUserNotificationCenter.current().delegate = self
    }

    func application(_ application: NSApplication, openFiles filenames: [String]) {
        for filename in filenames { handle(filename) }
        application.reply(toOpenOrPrint: .success)
    }

    private func finish(_ path: String, _ code: String?) {
        let receipt: [String: Any] = ["ok": code == nil, "code": code ?? "accepted", "pid": ProcessInfo.processInfo.processIdentifier]
        if let data = try? JSONSerialization.data(withJSONObject: receipt) {
            try? data.write(to: URL(fileURLWithPath: path + ".result"), options: .atomic)
        }
    }

    private func handle(_ path: String) {
        guard path.hasSuffix(".eigenflux-notification"),
              let attributes = try? FileManager.default.attributesOfItem(atPath: path),
              attributes[.type] as? FileAttributeType == .typeRegular,
              attributes[.ownerAccountID] as? UInt32 == getuid(),
              (attributes[.size] as? Int ?? Int.max) < 16384,
              let data = try? Data(contentsOf: URL(fileURLWithPath: path)),
              let request = try? JSONDecoder().decode(Request.self, from: data) else { return }
        let center = UNUserNotificationCenter.current()
        switch request.action {
        case "quit":
            self.finish(path, nil)
            DispatchQueue.main.async { NSApplication.shared.terminate(nil) }
        case "enable":
            center.requestAuthorization(options: [.alert, .sound]) { allowed, error in
                self.finish(path, error != nil ? "authorization_failed" : (allowed ? nil : "permission_denied"))
            }
        case "check", "show":
            center.getNotificationSettings { settings in
                guard settings.authorizationStatus == .authorized || settings.authorizationStatus == .provisional else {
                    self.finish(path, settings.authorizationStatus == .denied ? "permission_denied" : "permission_required")
                    return
                }
                guard request.action == "show" else { self.finish(path, nil); return }
                guard safeURL(request.message.url) != nil,
                      !request.message.id.isEmpty, request.message.id.count <= 256,
                      !request.message.title.isEmpty, request.message.title.count <= 200,
                      !request.message.body.isEmpty, request.message.body.count <= 2000 else {
                    self.finish(path, "invalid_message")
                    return
                }
                let content = UNMutableNotificationContent()
                content.title = request.message.title
                content.body = request.message.body
                content.sound = .default
                content.userInfo = ["url": request.message.url]
                center.add(UNNotificationRequest(identifier: request.message.id, content: content, trigger: nil)) { error in
                    self.finish(path, error == nil ? nil : "delivery_failed")
                }
            }
        default: finish(path, "invalid_action")
        }
    }

    func userNotificationCenter(_ center: UNUserNotificationCenter,
                                willPresent notification: UNNotification,
                                withCompletionHandler completionHandler: @escaping (UNNotificationPresentationOptions) -> Void) {
        completionHandler([.banner, .list, .sound])
    }

    func userNotificationCenter(_ center: UNUserNotificationCenter,
                                didReceive response: UNNotificationResponse,
                                withCompletionHandler completionHandler: @escaping () -> Void) {
        guard response.actionIdentifier == UNNotificationDefaultActionIdentifier,
              let raw = response.notification.request.content.userInfo["url"] as? String,
              let url = safeURL(raw) else { completionHandler(); return }
        DispatchQueue.main.async {
            NSWorkspace.shared.open(url)
            completionHandler()
        }
    }
}

let application = NSApplication.shared
let delegate = AppDelegate()
application.delegate = delegate
application.setActivationPolicy(.accessory)
application.run()
