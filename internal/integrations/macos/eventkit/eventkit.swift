// prism-eventkit: Calendar and Reminders for PRISM through EventKit (recurring events are expanded into
// their occurrences, which AppleScript cannot do). One JSON argument in, one JSON document out.
//
//   prism-eventkit <command> '<json>'      commands: selftest cals events add-event update-event delete-event
//                                          get-event get-reminder reminders add-reminder update-reminder complete-reminder delete-reminder
//
// Errors are printed as {"error":"…"} with exit status 1.
import Foundation
import EventKit

let helperVersion = 1
let store = EKEventStore()

// ── output ──────────────────────────────────────────────────────────────────

func emit(_ obj: Any) {
    guard let d = try? JSONSerialization.data(withJSONObject: obj, options: [.sortedKeys]) else { fail("cannot encode the result") }
    FileHandle.standardOutput.write(d)
    FileHandle.standardOutput.write("\n".data(using: .utf8)!)
}

func fail(_ msg: String) -> Never {
    let d = (try? JSONSerialization.data(withJSONObject: ["error": msg])) ?? Data("{\"error\":\"failed\"}".utf8)
    FileHandle.standardOutput.write(d)
    FileHandle.standardOutput.write("\n".data(using: .utf8)!)
    exit(1)
}

// ── dates ───────────────────────────────────────────────────────────────────

let isoOut: ISO8601DateFormatter = {
    let f = ISO8601DateFormatter()
    f.formatOptions = [.withInternetDateTime]
    f.timeZone = TimeZone.current
    return f
}()

func fmt(_ d: Date?) -> String? { d.map { isoOut.string(from: $0) } }

/// Accepts RFC 3339 with an offset or Z, or a local "YYYY-MM-DDTHH:MM[:SS]" / "YYYY-MM-DD HH:MM" / "YYYY-MM-DD".
func parseDate(_ s: String) -> Date? {
    if let d = ISO8601DateFormatter().date(from: s) { return d }
    let f = DateFormatter()
    f.locale = Locale(identifier: "en_US_POSIX")
    f.timeZone = TimeZone.current
    for p in ["yyyy-MM-dd'T'HH:mm:ss", "yyyy-MM-dd'T'HH:mm", "yyyy-MM-dd HH:mm", "yyyy-MM-dd"] {
        f.dateFormat = p
        if let d = f.date(from: s) { return d }
    }
    return nil
}

func isDateOnly(_ s: String) -> Bool { s.count == 10 && !s.contains("T") && !s.contains(" ") }

// ── access ──────────────────────────────────────────────────────────────────

func requireAccess(_ type: EKEntityType) {
    let sem = DispatchSemaphore(value: 0)
    var granted = false
    let done: (Bool, Error?) -> Void = { g, _ in granted = g; sem.signal() }
    if #available(macOS 14.0, *) {
        if type == .event { store.requestFullAccessToEvents(completion: done) } else { store.requestFullAccessToReminders(completion: done) }
    } else {
        store.requestAccess(to: type, completion: done)
    }
    if sem.wait(timeout: .now() + 75) == .timedOut {
        fail("waiting for the macOS permission prompt (answer it, then try again)")
    }
    if !granted {
        fail("access to \(type == .event ? "Calendars" : "Reminders") was not granted — allow it under System Settings → Privacy & Security → \(type == .event ? "Calendars" : "Reminders")")
    }
}

// ── arguments ───────────────────────────────────────────────────────────────

func args() -> [String: Any] {
    guard CommandLine.arguments.count > 2, let d = CommandLine.arguments[2].data(using: .utf8),
          let o = (try? JSONSerialization.jsonObject(with: d)) as? [String: Any] else { return [:] }
    return o
}
func str(_ a: [String: Any], _ k: String) -> String? {
    if let s = a[k] as? String, !s.trimmingCharacters(in: .whitespaces).isEmpty { return s }
    return nil
}
func int(_ a: [String: Any], _ k: String) -> Int? { (a[k] as? Int) ?? (a[k] as? Double).map { Int($0) } }
func bool(_ a: [String: Any], _ k: String) -> Bool? { a[k] as? Bool }
func strs(_ a: [String: Any], _ k: String) -> [String] { (a[k] as? [String]) ?? [] }

func calendars(_ type: EKEntityType, named: [String]) -> [EKCalendar] {
    let all = store.calendars(for: type)
    if named.isEmpty { return all }
    let want = named.map { $0.lowercased() }
    return all.filter { want.contains($0.title.lowercased()) }
}

func writableCalendar(_ type: EKEntityType, named: String?) -> EKCalendar {
    if let n = named {
        if let c = store.calendars(for: type).first(where: { $0.title.lowercased() == n.lowercased() }) {
            if !c.allowsContentModifications { fail("the calendar “\(c.title)” is read-only") }
            return c
        }
        fail("no calendar or list called “\(n)” (use calendar_list to see the names)")
    }
    if type == .event, let c = store.defaultCalendarForNewEvents { return c }
    if type == .reminder, let c = store.defaultCalendarForNewReminders() { return c }
    fail("there is no default \(type == .event ? "calendar" : "reminders list") — name one")
}

// ── events ──────────────────────────────────────────────────────────────────

func eventDict(_ e: EKEvent) -> [String: Any] {
    var d: [String: Any] = [
        "id": e.eventIdentifier ?? "", "title": e.title ?? "", "start": fmt(e.startDate) ?? "", "end": fmt(e.endDate) ?? "",
        "all_day": e.isAllDay, "calendar": e.calendar.title, "recurring": e.hasRecurrenceRules,
    ]
    if let l = e.location, !l.isEmpty { d["location"] = l }
    if let n = e.notes, !n.isEmpty { d["notes"] = String(n.prefix(600)) }
    if let u = e.url { d["url"] = u.absoluteString }
    if let a = e.attendees, !a.isEmpty { d["attendees"] = a.prefix(12).map { $0.name ?? $0.url.absoluteString } }
    if e.status == .canceled { d["cancelled"] = true }
    return d
}

func findEvent(_ a: [String: Any]) -> EKEvent {
    guard let id = str(a, "id") else { fail("id is required (take it from calendar_events)") }
    if let s = str(a, "occurrence"), let when = parseDate(s) {
        // the exact occurrence of a recurring event
        let p = store.predicateForEvents(withStart: when.addingTimeInterval(-60), end: when.addingTimeInterval(24 * 3600), calendars: nil)
        if let e = store.events(matching: p).first(where: { $0.eventIdentifier == id && abs($0.startDate.timeIntervalSince(when)) < 60 }) { return e }
    }
    guard let e = store.event(withIdentifier: id) else { fail("no event with that id (it may have been deleted)") }
    return e
}

func applyEvent(_ e: EKEvent, _ a: [String: Any], creating: Bool) {
    if let t = str(a, "title") { e.title = t }
    if let allDay = bool(a, "all_day") { e.isAllDay = allDay }
    if let s = str(a, "start") {
        guard let d = parseDate(s) else { fail("cannot read the start time “\(s)” (use 2026-09-22T14:00 or 2026-09-22)") }
        let dur = e.endDate != nil && e.startDate != nil ? e.endDate.timeIntervalSince(e.startDate) : 3600
        e.startDate = d
        if isDateOnly(s), bool(a, "all_day") == nil { e.isAllDay = true }
        if str(a, "end") == nil { e.endDate = creating ? d.addingTimeInterval(TimeInterval((int(a, "duration_min") ?? 60) * 60)) : d.addingTimeInterval(dur) }
    }
    if let s = str(a, "end") {
        guard let d = parseDate(s) else { fail("cannot read the end time “\(s)”") }
        e.endDate = d
    }
    if e.isAllDay, let s = e.startDate, e.endDate == nil || e.endDate < s { e.endDate = s }
    if let l = a["location"] as? String { e.location = l }
    if let n = a["notes"] as? String { e.notes = n }
    if let u = str(a, "url"), let url = URL(string: u) { e.url = url }
    if let alerts = a["alerts_min"] as? [Int] {
        e.alarms = alerts.map { EKAlarm(relativeOffset: -Double($0) * 60) }
    }
    if let r = str(a, "repeat") {
        let freq: EKRecurrenceFrequency
        switch r.lowercased() {
        case "daily": freq = .daily
        case "weekly": freq = .weekly
        case "monthly": freq = .monthly
        case "yearly": freq = .yearly
        case "none": e.recurrenceRules = nil; return
        default: fail("repeat must be none, daily, weekly, monthly or yearly")
        }
        var end: EKRecurrenceEnd? = nil
        if let n = int(a, "repeat_count") { end = EKRecurrenceEnd(occurrenceCount: n) }
        e.recurrenceRules = [EKRecurrenceRule(recurrenceWith: freq, interval: 1, end: end)]
    }
}

func spanOf(_ a: [String: Any]) -> EKSpan { (str(a, "span") ?? "this") == "future" ? .futureEvents : .thisEvent }

// ── reminders ───────────────────────────────────────────────────────────────

func fetchReminders(_ cals: [EKCalendar]?, completed: Bool?) -> [EKReminder] {
    let pred: NSPredicate
    switch completed {
    case .some(true): pred = store.predicateForCompletedReminders(withCompletionDateStarting: nil, ending: nil, calendars: cals)
    case .some(false): pred = store.predicateForIncompleteReminders(withDueDateStarting: nil, ending: nil, calendars: cals)
    case .none: pred = store.predicateForReminders(in: cals)
    }
    var out: [EKReminder] = []
    let sem = DispatchSemaphore(value: 0)
    store.fetchReminders(matching: pred) { r in out = r ?? []; sem.signal() }
    if sem.wait(timeout: .now() + 60) == .timedOut { fail("Reminders did not answer in time") }
    return out
}

func dueDate(_ r: EKReminder) -> Date? {
    guard let c = r.dueDateComponents else { return nil }
    return Calendar.current.date(from: c)
}

func reminderDict(_ r: EKReminder) -> [String: Any] {
    var d: [String: Any] = ["id": r.calendarItemIdentifier, "title": r.title ?? "", "list": r.calendar.title, "completed": r.isCompleted]
    if let due = dueDate(r) {
        d["due"] = fmt(due) ?? ""
        d["due_has_time"] = r.dueDateComponents?.hour != nil
    }
    if r.priority > 0 { d["priority"] = r.priority }
    if let n = r.notes, !n.isEmpty { d["notes"] = String(n.prefix(400)) }
    return d
}

func findReminder(_ a: [String: Any]) -> EKReminder {
    guard let id = str(a, "id"), let r = store.calendarItem(withIdentifier: id) as? EKReminder else {
        fail("no reminder with that id (take it from reminders_list)")
    }
    return r
}

func applyReminder(_ r: EKReminder, _ a: [String: Any]) {
    if let t = str(a, "title") { r.title = t }
    if let n = a["notes"] as? String { r.notes = n }
    if let p = int(a, "priority") { r.priority = min(max(p, 0), 9) }
    if let s = str(a, "due") {
        guard let d = parseDate(s) else { fail("cannot read the due time “\(s)” (use 2026-09-22T14:00 or 2026-09-22)") }
        let units: Set<Calendar.Component> = isDateOnly(s) ? [.year, .month, .day] : [.year, .month, .day, .hour, .minute]
        r.dueDateComponents = Calendar.current.dateComponents(units, from: d)
        r.alarms = nil
        if !isDateOnly(s) { r.addAlarm(EKAlarm(absoluteDate: d)) } // so it actually notifies
    }
    if str(a, "due") == nil, let clear = bool(a, "clear_due"), clear { r.dueDateComponents = nil; r.alarms = nil }
}

// ── commands ────────────────────────────────────────────────────────────────

guard CommandLine.arguments.count > 1 else { fail("usage: prism-eventkit <command> '<json>'") }
let cmd = CommandLine.arguments[1]
let a = args()

switch cmd {
case "selftest":
    emit(["ok": true, "version": helperVersion, "events_status": EKEventStore.authorizationStatus(for: .event).rawValue,
          "reminders_status": EKEventStore.authorizationStatus(for: .reminder).rawValue])

case "cals":
    requireAccess(.event)
    requireAccess(.reminder)
    func row(_ c: EKCalendar, _ kind: String) -> [String: Any] {
        ["title": c.title, "kind": kind, "writable": c.allowsContentModifications, "source": c.source?.title ?? ""]
    }
    emit(["calendars": store.calendars(for: .event).map { row($0, "events") } + store.calendars(for: .reminder).map { row($0, "reminders") }])

case "events":
    requireAccess(.event)
    let from = str(a, "from").flatMap(parseDate) ?? Calendar.current.startOfDay(for: Date())
    var to = str(a, "to").flatMap(parseDate) ?? from.addingTimeInterval(7 * 24 * 3600)
    if let t = str(a, "to"), isDateOnly(t) { to = to.addingTimeInterval(24 * 3600 - 1) } // a bare date means "through that day"
    if to <= from { fail("\"to\" must be after \"from\"") }
    if to.timeIntervalSince(from) > 366 * 24 * 3600 { fail("ask for at most a year at a time") }
    let cals = calendars(.event, named: strs(a, "calendars"))
    if !strs(a, "calendars").isEmpty && cals.isEmpty { fail("none of those calendars exist (use calendar_list)") }
    var evs = store.events(matching: store.predicateForEvents(withStart: from, end: to, calendars: cals))
    if let q = str(a, "query")?.lowercased() {
        evs = evs.filter { ($0.title ?? "").lowercased().contains(q) || ($0.location ?? "").lowercased().contains(q) || ($0.notes ?? "").lowercased().contains(q) }
    }
    evs.sort { $0.startDate < $1.startDate }
    let limit = min(max(int(a, "limit") ?? 60, 1), 300)
    emit(["events": evs.prefix(limit).map(eventDict), "total": evs.count])

case "get-event":
    requireAccess(.event)
    emit(["event": eventDict(findEvent(a))])

case "get-reminder":
    requireAccess(.reminder)
    emit(["reminder": reminderDict(findReminder(a))])

case "add-event":
    requireAccess(.event)
    guard str(a, "title") != nil, str(a, "start") != nil else { fail("title and start are required") }
    let e = EKEvent(eventStore: store)
    e.calendar = writableCalendar(.event, named: str(a, "calendar"))
    applyEvent(e, a, creating: true)
    if e.alarms == nil || e.alarms!.isEmpty, let m = int(a, "default_alert_min"), m > 0 { e.alarms = [EKAlarm(relativeOffset: -Double(m) * 60)] }
    do { try store.save(e, span: .thisEvent, commit: true) } catch { fail("could not save the event: \(error.localizedDescription)") }
    emit(["event": eventDict(e)])

case "update-event":
    requireAccess(.event)
    let e = findEvent(a)
    if !e.calendar.allowsContentModifications { fail("that event is in a read-only calendar") }
    if let c = str(a, "calendar_to") { e.calendar = writableCalendar(.event, named: c) }
    applyEvent(e, a, creating: false)
    do { try store.save(e, span: spanOf(a), commit: true) } catch { fail("could not update the event: \(error.localizedDescription)") }
    emit(["event": eventDict(e)])

case "delete-event":
    requireAccess(.event)
    let e = findEvent(a)
    if !e.calendar.allowsContentModifications { fail("that event is in a read-only calendar") }
    let d = eventDict(e)
    do { try store.remove(e, span: spanOf(a), commit: true) } catch { fail("could not delete the event: \(error.localizedDescription)") }
    emit(["deleted": d])

case "reminders":
    requireAccess(.reminder)
    let cals = calendars(.reminder, named: strs(a, "lists"))
    if !strs(a, "lists").isEmpty && cals.isEmpty { fail("none of those lists exist (use calendar_list)") }
    let completed: Bool? = bool(a, "completed") == true ? true : (bool(a, "include_completed") == true ? nil : false)
    var rs = fetchReminders(cals.isEmpty ? nil : cals, completed: completed)
    if let q = str(a, "query")?.lowercased() { rs = rs.filter { ($0.title ?? "").lowercased().contains(q) || ($0.notes ?? "").lowercased().contains(q) } }
    rs.sort {
        switch (dueDate($0), dueDate($1)) {
        case let (x?, y?): return x < y
        case (_?, nil): return true
        case (nil, _?): return false
        default: return ($0.title ?? "") < ($1.title ?? "")
        }
    }
    let limit = min(max(int(a, "limit") ?? 50, 1), 300)
    emit(["reminders": rs.prefix(limit).map(reminderDict), "total": rs.count])

case "add-reminder":
    requireAccess(.reminder)
    guard str(a, "title") != nil else { fail("title is required") }
    let r = EKReminder(eventStore: store)
    r.calendar = writableCalendar(.reminder, named: str(a, "list"))
    applyReminder(r, a)
    do { try store.save(r, commit: true) } catch { fail("could not save the reminder: \(error.localizedDescription)") }
    emit(["reminder": reminderDict(r)])

case "update-reminder":
    requireAccess(.reminder)
    let r = findReminder(a)
    if let l = str(a, "list_to") { r.calendar = writableCalendar(.reminder, named: l) }
    applyReminder(r, a)
    do { try store.save(r, commit: true) } catch { fail("could not update the reminder: \(error.localizedDescription)") }
    emit(["reminder": reminderDict(r)])

case "complete-reminder":
    requireAccess(.reminder)
    let r = findReminder(a)
    r.isCompleted = bool(a, "undo") == true ? false : true
    do { try store.save(r, commit: true) } catch { fail("could not update the reminder: \(error.localizedDescription)") }
    emit(["reminder": reminderDict(r)])

case "delete-reminder":
    requireAccess(.reminder)
    let r = findReminder(a)
    let d = reminderDict(r)
    do { try store.remove(r, commit: true) } catch { fail("could not delete the reminder: \(error.localizedDescription)") }
    emit(["deleted": d])

default:
    fail("unknown command \(cmd)")
}
