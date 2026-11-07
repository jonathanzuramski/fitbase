// Shared rendering helpers for AI coach output — the dashboard's insights and
// chat, and the calendar's weekly plan. Load after marked.min.js and
// purify.min.js. Everything here is a plain global, as before the extraction.

// strictStrikethrough replaces marked's built-in strikethrough rule.
//
// Out of the box, marked treats a single "~" as strikethrough, so a coach
// reply like "hold ~250W for ~20 min" rendered "250W for " with a line
// through it. This version only recognises the two-tilde form ~~like this~~;
// a lone "~" (the coach uses it for "approximately") is left as plain text.
//
// marked calls this with the not-yet-parsed remainder of the current line.
// It must return either a token describing what it matched at the very
// start of that text, or undefined to say "no strikethrough here".
function strictStrikethrough(remainingText) {
	// "~~", then one or more characters that are not "~", then "~~".
	const match = /^~~([^~]+)~~/.exec(remainingText);
	if (!match) {
		return undefined;
	}
	const matchedText = match[0]; // e.g. "~~old plan~~"
	const innerText = match[1]; // e.g. "old plan"
	return {
		type: 'del',
		raw: matchedText, // how much of remainingText marked should consume
		text: innerText,
		tokens: this.lexer.inlineTokens(innerText),
	};
}
if (window.marked) {
	window.marked.use({ tokenizer: { del: strictStrikethrough } });
}

// renderSafeMarkdown converts untrusted LLM output (markdown) to HTML and
// sanitizes it: marked builds the HTML, DOMPurify strips any embedded
// <script>, event-handler attributes, and javascript: URIs so a crafted coach
// reply — or attacker-influenced workout text echoed back by the model —
// cannot execute in the page. Falls back to escaped plain text if either
// library fails to load, so output is never rendered unsanitized.
function renderSafeMarkdown(md) {
	if (window.marked && window.DOMPurify) {
		return window.DOMPurify.sanitize(window.marked.parse(md));
	}
	return escapeHTML(md);
}

// Parse a chunk of SSE text. Events in the buffer are separated by blank
// lines; return the unconsumed tail for the next read.
function parseSSE(buf, onEvent) {
	const events = buf.split(/\n\n/);
	const tail = events.pop();
	for (const raw of events) {
		let event = 'message', data = '';
		for (const line of raw.split('\n')) {
			if (line.startsWith('event:')) event = line.slice(6).trim();
			else if (line.startsWith('data:')) data += line.slice(5).trim();
		}
		if (data) {
			try { onEvent(event, JSON.parse(data)); } catch (_) { }
		}
	}
	return tail;
}

// markdownStreamer coalesces streamed-markdown re-renders into el: chunks
// arrive far faster than the display paints, and re-parsing the whole
// accumulated markdown per chunk is O(n²). update() renders the latest text
// at most once per animation frame; stop() abandons any pending paint (for
// error paths that replace el's content). onPaint, if given, runs after each
// paint (e.g. to keep a log scrolled to the bottom).
function markdownStreamer(el, onPaint) {
	let latest = '', scheduled = false, stopped = false;
	return {
		update(md) {
			latest = md;
			if (scheduled || stopped) return;
			scheduled = true;
			requestAnimationFrame(() => {
				scheduled = false;
				if (stopped) return;
				el.innerHTML = renderSafeMarkdown(latest);
				if (onPaint) onPaint();
			});
		},
		stop() { stopped = true; },
	};
}

function escapeHTML(s) {
	const d = document.createElement('div');
	d.textContent = s;
	return d.innerHTML;
}
