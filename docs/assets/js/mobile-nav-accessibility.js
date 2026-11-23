(function () {
  "use strict";

  function makeMobileNavigationAccessible() {
    const toggle = document.querySelector(
      '.wy-nav-top [data-toggle="wy-nav-top"]',
    );
    const sideNav = document.querySelector(
      'nav.wy-nav-side[data-toggle="wy-nav-shift"]',
    );

    if (!toggle || !sideNav) return;

    if (!sideNav.id) sideNav.id = "wy-nav-side";

    toggle.setAttribute("role", "button");
    toggle.setAttribute("tabindex", "0");
    toggle.setAttribute("aria-label", "Toggle navigation");
    toggle.setAttribute("aria-controls", sideNav.id);

    function syncExpandedState() {
      toggle.setAttribute(
        "aria-expanded",
        sideNav.classList.contains("shift") ? "true" : "false",
      );
    }

    syncExpandedState();

    new MutationObserver(syncExpandedState).observe(sideNav, {
      attributes: true,
      attributeFilter: ["class"],
    });

    toggle.addEventListener("keydown", function (event) {
      if (event.key !== "Enter" && event.key !== " ") return;
      event.preventDefault();
      // Use the theme's existing click handler so pointer and keyboard
      // activation follow the same navigation behavior.
      toggle.click();
    });
  }

  if (document.readyState === "loading") {
    document.addEventListener("DOMContentLoaded", makeMobileNavigationAccessible, {
      once: true,
    });
  } else {
    makeMobileNavigationAccessible();
  }
})();
