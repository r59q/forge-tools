/* Animated Tabs — Slide indicator and switch panels */

(function () {
  var containers = document.querySelectorAll('.nuda-tabs');
  containers.forEach(function (tabs) {
    var tabBtns = tabs.querySelectorAll('.nuda-tabs__tab');
    var panels = tabs.querySelectorAll('.nuda-tabs__panel');
    var indicator = tabs.querySelector('.nuda-tabs__indicator');
    if (!tabBtns.length || !indicator) return;

    tabBtns.forEach(function (btn, i) {
      btn.addEventListener('click', function () {
        tabBtns.forEach(function (b) {
          b.classList.remove('is-active');
          b.setAttribute('aria-selected', 'false');
        });
        panels.forEach(function (p) { p.classList.remove('is-active'); });

        btn.classList.add('is-active');
        btn.setAttribute('aria-selected', 'true');
        var panel = tabs.querySelector('[data-panel="' + btn.dataset.tab + '"]');
        if (panel) panel.classList.add('is-active');

        indicator.style.transform = 'translateX(' + (i * 100) + '%)';
      });
    });
  });
})();