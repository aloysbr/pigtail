import Foundation
@testable import MenuBarCore

/// Stub client, modelled on the monitors' stub readers with the gating of
/// `GatedDaemonClient` (PopoverStoreTests): it counts the calls, can park the
/// next GET and the next POST (once each), and says when a parked call has
/// entered — the race is produced by the test, never raced for.
actor StubLid: LidReading {
    struct SetCall: Equatable { let on: Bool; let hours: Int? }

    private var getResult: Result<LidResponse, Error>
    private var postResult: Result<LidResponse, Error>
    private(set) var getCalls = 0
    private(set) var setCalls: [SetCall] = []

    private var holdGet = false
    private var holdPost = false
    private var parkedGet: CheckedContinuation<Result<LidResponse, Error>, Never>?
    private var parkedPost: CheckedContinuation<Result<LidResponse, Error>, Never>?
    private var getEntered = false
    private var postEntered = false
    private var getEntryWaiter: CheckedContinuation<Void, Never>?
    private var postEntryWaiter: CheckedContinuation<Void, Never>?

    init(get: Result<LidResponse, Error>,
         post: Result<LidResponse, Error> = .success(LidResponse(sleepDisabled: false))) {
        getResult = get; postResult = post
    }

    func setGet(_ r: Result<LidResponse, Error>) { getResult = r }
    func setPost(_ r: Result<LidResponse, Error>) { postResult = r }
    func holdNextGet() { holdGet = true; getEntered = false }
    func holdNextPost() { holdPost = true; postEntered = false }

    func awaitGetEntry() async {
        if getEntered { return }
        await withCheckedContinuation { getEntryWaiter = $0 }
    }
    func awaitPostEntry() async {
        if postEntered { return }
        await withCheckedContinuation { postEntryWaiter = $0 }
    }
    func releaseGet(_ r: Result<LidResponse, Error>) { parkedGet?.resume(returning: r); parkedGet = nil }
    func releasePost(_ r: Result<LidResponse, Error>) { parkedPost?.resume(returning: r); parkedPost = nil }

    func lid() async throws -> LidResponse {
        getCalls += 1
        if holdGet {
            holdGet = false
            let r = await withCheckedContinuation { (c: CheckedContinuation<Result<LidResponse, Error>, Never>) in
                parkedGet = c
                getEntered = true
                getEntryWaiter?.resume(); getEntryWaiter = nil
            }
            return try r.get()
        }
        return try getResult.get()
    }

    func setLid(on: Bool, hours: Int?) async throws -> LidResponse {
        setCalls.append(SetCall(on: on, hours: hours))
        if holdPost {
            holdPost = false
            let r = await withCheckedContinuation { (c: CheckedContinuation<Result<LidResponse, Error>, Never>) in
                parkedPost = c
                postEntered = true
                postEntryWaiter?.resume(); postEntryWaiter = nil
            }
            return try r.get()
        }
        return try postResult.get()
    }
}
