// shared probe: returns every element whose text is visually cut
export const PROBE = `(() => {
  const out = [];
  const path = (el) => {
    const parts = [];
    let n = el;
    while (n && n.nodeType === 1 && parts.length < 5) {
      let s = n.tagName.toLowerCase();
      if (n.id) s += '#' + n.id;
      if (n.className && typeof n.className === 'string') s += '.' + n.className.trim().split(/\\s+/).slice(0,4).join('.');
      parts.unshift(s);
      n = n.parentElement;
    }
    return parts.join(' > ');
  };
  const all = document.querySelectorAll('body *');
  for (const el of all) {
    const cs = getComputedStyle(el);
    if (cs.display === 'none' || cs.visibility === 'hidden' || parseFloat(cs.opacity) === 0) continue;
    const r = el.getBoundingClientRect();
    if (r.width < 1 || r.height < 1) continue;

    // only innermost text holders: element has at least one text child with content
    let own = '';
    for (const n of el.childNodes) if (n.nodeType === 3) own += n.nodeValue;
    const isInput = el.tagName === 'INPUT' || el.tagName === 'TEXTAREA';
    if (!own.trim() && !isInput) continue;

    const full = isInput ? (el.value || el.placeholder || '') : (el.textContent || '').trim();
    if (!full) continue;

    // --- A: own-box overflow
    const selfOverflowX = el.scrollWidth - el.clientWidth;
    const selfOverflowY = el.scrollHeight - el.clientHeight;

    // --- B: rendered text extent vs clipping ancestors
    let textRect = null;
    if (!isInput) {
      try {
        const rg = document.createRange();
        rg.selectNodeContents(el);
        const rects = Array.from(rg.getClientRects());
        if (rects.length) {
          const l = Math.min(...rects.map(x => x.left));
          const rr = Math.max(...rects.map(x => x.right));
          const t = Math.min(...rects.map(x => x.top));
          const b = Math.max(...rects.map(x => x.bottom));
          textRect = { left: l, right: rr, top: t, bottom: b, width: rr - l, height: b - t };
        }
      } catch (e) {}
    }

    let hiddenRight = 0, hiddenLeft = 0, hiddenBottom = 0, clipper = null, clipperStyle = null;
    if (textRect) {
      let n = el;
      while (n && n !== document.body) {
        const ns = getComputedStyle(n);
        const ox = ns.overflowX, oy = ns.overflowY;
        const clipsX = ox === 'hidden' || ox === 'clip';
        const clipsY = oy === 'hidden' || oy === 'clip';
        if (clipsX || clipsY) {
          const nr = n.getBoundingClientRect();
          const hr = clipsX ? Math.max(0, textRect.right - (nr.right - 0.5)) : 0;
          const hl = clipsX ? Math.max(0, (nr.left + 0.5) - textRect.left) : 0;
          const hb = clipsY ? Math.max(0, textRect.bottom - (nr.bottom - 0.5)) : 0;
          if (hr > hiddenRight || hl > hiddenLeft || hb > hiddenBottom) {
            if (hr > 1 || hl > 1 || hb > 1) { clipper = path(n); clipperStyle = { overflowX: ox, overflowY: oy, textOverflow: ns.textOverflow, whiteSpace: ns.whiteSpace, lineClamp: ns.webkitLineClamp, w: Math.round(nr.width), h: Math.round(nr.height) }; }
          }
          hiddenRight = Math.max(hiddenRight, hr);
          hiddenLeft = Math.max(hiddenLeft, hl);
          hiddenBottom = Math.max(hiddenBottom, hb);
        }
        n = n.parentElement;
      }
    }

    // input clipping: scrollWidth vs clientWidth
    let inputHidden = 0;
    if (isInput) {
      inputHidden = Math.max(0, el.scrollWidth - el.clientWidth);
      // placeholder measurement done separately
    }

    const ellipsised = cs.textOverflow === 'ellipsis' && (cs.overflowX === 'hidden' || cs.overflowX === 'clip') && selfOverflowX > 1;
    const cut = hiddenRight > 1 || hiddenLeft > 1 || hiddenBottom > 1 || ellipsised || (isInput && inputHidden > 1);
    if (!cut) continue;

    out.push({
      path: path(el),
      tag: el.tagName.toLowerCase(),
      cls: (typeof el.className === 'string' ? el.className : ''),
      text: full.slice(0, 300),
      textLen: full.length,
      isInput,
      isPlaceholder: isInput && !el.value && !!el.placeholder,
      box: { w: Math.round(r.width * 10) / 10, h: Math.round(r.height * 10) / 10, x: Math.round(r.x), y: Math.round(r.y) },
      clientWidth: el.clientWidth,
      scrollWidth: el.scrollWidth,
      clientHeight: el.clientHeight,
      scrollHeight: el.scrollHeight,
      selfOverflowX, selfOverflowY,
      textWidth: textRect ? Math.round(textRect.width * 10) / 10 : null,
      hiddenRight: Math.round(hiddenRight * 10) / 10,
      hiddenLeft: Math.round(hiddenLeft * 10) / 10,
      hiddenBottom: Math.round(hiddenBottom * 10) / 10,
      inputHidden,
      style: { overflowX: cs.overflowX, overflowY: cs.overflowY, textOverflow: cs.textOverflow, whiteSpace: cs.whiteSpace, lineClamp: cs.webkitLineClamp, fontSize: cs.fontSize, font: cs.fontFamily.split(',')[0] },
      clipper, clipperStyle,
    });
  }
  return out;
})()`;

// measures how wide a placeholder WANTS to be, vs the field's content box
export const PLACEHOLDER_PROBE = `(() => {
  const out = [];
  const fields = document.querySelectorAll('input[placeholder], textarea[placeholder]');
  const c = document.createElement('canvas').getContext('2d');
  for (const el of fields) {
    const cs = getComputedStyle(el);
    if (cs.display === 'none' || cs.visibility === 'hidden') continue;
    const r = el.getBoundingClientRect();
    if (r.width < 1 || r.height < 1) continue;
    c.font = cs.fontStyle + ' ' + cs.fontWeight + ' ' + cs.fontSize + ' ' + cs.fontFamily;
    const w = c.measureText(el.placeholder).width;
    const avail = el.clientWidth - parseFloat(cs.paddingLeft) - parseFloat(cs.paddingRight);
    if (w > avail + 0.5) {
      // how many chars fit
      let fit = 0;
      for (let i = 1; i <= el.placeholder.length; i++) {
        if (c.measureText(el.placeholder.slice(0, i)).width <= avail) fit = i; else break;
      }
      out.push({
        placeholder: el.placeholder,
        visible: el.placeholder.slice(0, fit),
        hiddenChars: el.placeholder.length - fit,
        needPx: Math.round(w * 10) / 10,
        availPx: Math.round(avail * 10) / 10,
        shortPx: Math.round((w - avail) * 10) / 10,
        boxW: Math.round(r.width * 10) / 10,
        cls: (typeof el.className === 'string' ? el.className : ''),
        font: cs.fontSize + ' ' + cs.fontFamily.split(',')[0],
        id: el.id || null, name: el.name || null,
      });
    }
  }
  return out;
})()`;
