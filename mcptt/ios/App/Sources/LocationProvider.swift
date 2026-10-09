// Location capture for emergencies — the spec's "location attached to the
// dispatcher alert". One-shot best-effort fix; failures report nil and the
// alert goes out without coordinates rather than not going out.

import Foundation
import CoreLocation
import MCPTTCore

final class LocationProvider: NSObject, CLLocationManagerDelegate {
    private let manager = CLLocationManager()
    private var continuation: CheckedContinuation<(Double, Double)?, Never>?

    override init() {
        super.init()
        manager.delegate = self
        manager.desiredAccuracy = kCLLocationAccuracyHundredMeters
    }

    /// Requests a one-shot location. Returns nil when permission is denied
    /// or no fix arrives — never blocks the alert path for long.
    func currentLocation() async -> (Double, Double)? {
        let status = manager.authorizationStatus
        if status == .notDetermined {
            manager.requestWhenInUseAuthorization()
        }
        if status == .denied || status == .restricted {
            return nil
        }
        return await withCheckedContinuation { continuation in
            self.continuation = continuation
            self.manager.requestLocation()
        }
    }

    func locationManager(_ manager: CLLocationManager, didUpdateLocations locations: [CLLocation]) {
        let fix = locations.last.map { ($0.coordinate.latitude, $0.coordinate.longitude) }
        continuation?.resume(returning: fix)
        continuation = nil
    }

    func locationManager(_ manager: CLLocationManager, didFailWithError error: Error) {
        continuation?.resume(returning: nil)
        continuation = nil
    }
}
