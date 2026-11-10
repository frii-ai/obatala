(function () {
  "use strict";
  var root = document.querySelector(".scenarios");
  if (!root) return;
  var cards = Array.prototype.slice.call(root.querySelectorAll(".scenario"));
  // Suite-folder groups: remembered so "run order" can restore cards into
  // their groups after another sort flattened them.
  var groupState = Array.prototype.slice.call(root.querySelectorAll(".group")).map(function (g) {
    return { el: g, cards: Array.prototype.slice.call(g.querySelectorAll(".scenario")) };
  });
  var sortMode = "run";
  var q = document.getElementById("q");
  var count = document.getElementById("count");
  var sortSel = document.getElementById("sort");
  var outcomeChips = document.getElementById("outcome-chips");
  var tagChips = document.getElementById("tag-chips");
  var outcome = "all";
  var activeTags = {};

  function isDetailOpen(card) {
    return card.dataset.outcome !== "pass";
  }

  cards.forEach(function (card) {
    var head = card.querySelector(".scenario-head");
    var open = isDetailOpen(card);
    card.classList.toggle("open", open);
    head.setAttribute("aria-expanded", open ? "true" : "false");
    head.addEventListener("click", function () {
      var nowOpen = card.classList.toggle("open");
      head.setAttribute("aria-expanded", nowOpen ? "true" : "false");
    });
  });

  function matches(card) {
    if (outcome !== "all" && card.dataset.outcome !== outcome) return false;
    var text = q && q.value ? q.value.trim().toLowerCase() : "";
    if (text) {
      var hay = card.dataset.id.toLowerCase() + " " +
        (card.dataset.name || "").toLowerCase();
      if (hay.indexOf(text) < 0) return false;
    }
    var wanted = Object.keys(activeTags).filter(function (t) { return activeTags[t]; });
    var tags = card.dataset.tags ? card.dataset.tags.split(" ") : [];
    return wanted.every(function (t) { return tags.indexOf(t) >= 0; });
  }

  function updateGroups() {
    groupState.forEach(function (st) {
      if (sortMode !== "run") {
        st.el.hidden = true;
        return;
      }
      st.el.hidden = !st.cards.some(function (c) { return !c.hidden; });
    });
  }

  function apply() {
    var shown = 0;
    cards.forEach(function (card) {
      var ok = matches(card);
      card.hidden = !ok;
      if (ok) shown++;
    });
    updateGroups();
    if (count) count.textContent = shown + " / " + cards.length;
  }

  function chipGroup(container, onPick) {
    if (!container) return;
    container.addEventListener("click", function (e) {
      var btn = e.target.closest("button");
      if (!btn || !container.contains(btn)) return;
      onPick(btn);
      apply();
    });
  }

  chipGroup(outcomeChips, function (btn) {
    outcome = btn.dataset.outcome;
    Array.prototype.forEach.call(outcomeChips.querySelectorAll("button"), function (b) {
      b.classList.toggle("on", b === btn);
    });
  });

  chipGroup(tagChips, function (btn) {
    var tag = btn.dataset.tag;
    activeTags[tag] = !activeTags[tag];
    btn.classList.toggle("on", !!activeTags[tag]);
  });

  function byId(a, b) {
    return a.dataset.id < b.dataset.id ? -1 : a.dataset.id > b.dataset.id ? 1 : 0;
  }

  function sort(mode) {
    sortMode = mode;
    var ordered = cards.slice();
    ordered.sort(function (a, b) {
      switch (mode) {
        case "outcome":
          return (+a.dataset.rank - +b.dataset.rank) || byId(a, b);
        case "latency":
          return (+b.dataset.latency - +a.dataset.latency) || byId(a, b);
        case "duration":
          return (+b.dataset.duration - +a.dataset.duration) || byId(a, b);
        case "turns":
          return (+b.dataset.turns - +a.dataset.turns) || byId(a, b);
        case "id":
          return byId(a, b);
        default: // "run": the order scenarios finished in
          return cards.indexOf(a) - cards.indexOf(b);
      }
    });
    if (mode === "run") {
      // Regroup: cards go back into their suite folders in run order.
      groupState.forEach(function (st) {
        st.cards.forEach(function (card) { st.el.appendChild(card); });
      });
    } else {
      // Sorting crosses folders: flatten so groups hold nothing.
      ordered.forEach(function (card) { root.appendChild(card); });
    }
    apply();
  }

  if (q) q.addEventListener("input", apply);
  if (sortSel) {
    sortSel.addEventListener("change", function () { sort(sortSel.value); });
    sort(sortSel.value);
  }
  apply();
})();
