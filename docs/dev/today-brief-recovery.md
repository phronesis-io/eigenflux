# Today brief recovery

A model timeout or a slightly long sentence can leave the Today card empty for an hour. The dashboard should show the best available account-specific information throughout a refresh.

* Aim for 60 Chinese or 120 English characters. Compress once, then allow an intact sentence up to 90 or 180 Unicode characters respectively. Render wrapping text on narrow screens.
* While regenerating, or if the refresh fails, show the last successful brief for the same Agent, working language and local day. Display its generation time. On a first failure show a summary drawn from the current overview.
* When a first attempt fails, a visible page requests a new overview after one minute. Multiple tabs share a single server claim. A second failure uses the ordinary hourly interval. Subsequent opens can recover interrupted attempts after the existing two-minute lease.
* Reproduce overlength, timeout with and without earlier text, duplicate claims, and rollover. Verify with the normal overview and polling paths. Track failures by reason and count successful current-day rows after release.
* Release the compatible API first, then both website targets. Roll back affected binaries or static bundles to their previous releases if authentication, overview reads or rendering regress.
