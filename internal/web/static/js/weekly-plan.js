// Weekly plan panel on the calendar page: run "plan my week", stream the
// coach's review live, show the proposed week, accept / discard / revise it,
// and toggle the Sunday-night auto-plan. Needs coach-render.js
// (renderSafeMarkdown, parseSSE, markdownStreamer, escapeHTML).
(function () {
	const $ = (id) => document.getElementById(id);
	const auto = $('wp-auto'), next = $('wp-next'), intro = $('wp-intro');
	const notes = $('wp-notes'), runBtn = $('wp-run'), reviseBtn = $('wp-revise');
	const meta = $('wp-meta'), status = $('wp-status'), review = $('wp-review');
	const week = $('wp-week'), actions = $('wp-actions');
	const acceptBtn = $('wp-accept'), discardBtn = $('wp-discard'), errEl = $('wp-error');
	if (!runBtn) return;

	let current = null; // the plan on screen (null when none)
	let busy = false;

	const readyPlaceholder = 'change something? e.g. move the long ride to Sunday';
	const freshPlaceholder = notes.placeholder;

	function showError(msg) {
		errEl.textContent = msg || '';
		errEl.hidden = !msg;
	}

	function setBusy(b) {
		busy = b;
		runBtn.disabled = reviseBtn.disabled = notes.disabled = b;
		acceptBtn.disabled = discardBtn.disabled = b;
	}

	// Plan dates are rider-local calendar days ("2026-11-09"); parse them as
	// local midnight so the weekday never shifts with the viewer's UTC offset.
	function fmtDay(iso) {
		const [y, m, d] = (iso || '').slice(0, 10).split('-').map(Number);
		if (!y) return '';
		return new Date(y, m - 1, d).toLocaleDateString(undefined, { weekday: 'short', month: 'short', day: 'numeric' });
	}

	async function api(path, opts) {
		const res = await fetch(path, opts);
		let body = null;
		try { body = await res.json(); } catch (_) { }
		if (!res.ok) throw new Error((body && body.error) || ('HTTP ' + res.status));
		return body ? (body.data !== undefined ? body.data : body) : null;
	}

	function renderSchedule(s) {
		auto.checked = !!(s && s.enabled);
		if (s && s.enabled && s.next_run) {
			const when = new Date(s.next_run).toLocaleString(undefined, { weekday: 'long', month: 'short', day: 'numeric', hour: 'numeric', minute: '2-digit' });
			next.textContent = 'Next auto-plan: ' + when + '. You’ll find it here to review — nothing is added to your calendar until you accept it.';
			next.hidden = false;
		} else {
			next.hidden = true;
		}
	}

	function renderWeek(ws) {
		week.innerHTML = '';
		for (const p of ws) {
			const item = document.createElement('div');
			item.className = 'coach-preview-item';
			const dur = Math.round((p.duration_secs || 0) / 60);
			const tss = p.tss ? ' · ' + Math.round(p.tss) + ' TSS' : '';
			item.innerHTML =
				'<div class="coach-preview-date">' + escapeHTML(fmtDay(p.planned_date)) + ' · ' + escapeHTML(p.sport || 'cycling') + '</div>' +
				'<div class="coach-preview-main">' +
				'<span class="coach-preview-title-line">' + escapeHTML(p.title || '(untitled)') + '</span>' +
				'<span class="coach-preview-meta">' + dur + ' min' + tss + '</span>' +
				'</div>' +
				(p.description ? '<div class="coach-preview-desc">' + escapeHTML(p.description) + '</div>' : '');
			week.appendChild(item);
		}
		week.hidden = ws.length === 0;
	}

	function renderPlan(plan, workouts) {
		current = plan;
		status.hidden = true;
		if (!plan) {
			intro.hidden = false;
			meta.hidden = review.hidden = week.hidden = actions.hidden = reviseBtn.hidden = true;
			notes.placeholder = freshPlaceholder;
			return;
		}
		intro.hidden = true;
		const created = new Date(plan.created_at).toLocaleString(undefined, { weekday: 'short', month: 'short', day: 'numeric', hour: 'numeric', minute: '2-digit' });
		const how = plan.trigger === 'scheduled' ? 'auto-planned ' : 'planned ';
		const state = plan.status === 'accepted' ? ' · added to your calendar' : '';
		meta.textContent = fmtDay(plan.window_start) + ' – ' + fmtDay(plan.window_end) + ' · ' + how + created + state;
		meta.hidden = false;
		review.innerHTML = renderSafeMarkdown(plan.review_md || '');
		review.hidden = !plan.review_md;
		renderWeek(workouts || []);
		const ready = plan.status === 'ready';
		actions.hidden = !(ready && workouts && workouts.length);
		reviseBtn.hidden = !ready;
		notes.placeholder = ready ? readyPlaceholder : freshPlaceholder;
	}

	async function load() {
		const d = await api('/api/coach/weekly-plan');
		renderSchedule(d.schedule);
		// A discarded plan is dismissed: show the empty state, not the old week.
		const plan = d.plan && d.plan.status !== 'discarded' ? d.plan : null;
		renderPlan(plan, d.workouts);
	}

	function setStatus(running) {
		const labels = Object.values(running);
		status.textContent = labels.length ? 'looking up ' + labels.join(', ') + '…' : 'thinking…';
	}

	// stream consumes one plan run's SSE — a new run or a re-attach to one that
	// is already going (including a Sunday-night auto-plan). The run itself is
	// detached server-side: leaving the page doesn't cancel it.
	async function stream(fetcher) {
		setBusy(true);
		showError('');
		intro.hidden = meta.hidden = week.hidden = actions.hidden = true;
		review.innerHTML = '';
		review.hidden = false;
		status.hidden = false;
		const running = {};
		setStatus(running);

		let md = '', streamErr = null;
		const streamer = markdownStreamer(review);
		try {
			const res = await fetcher();
			if (!res.ok || !res.body) {
				const j = await res.json().catch(() => ({}));
				throw new Error(j.error || 'request failed');
			}
			const reader = res.body.getReader();
			const decoder = new TextDecoder();
			let buf = '';
			while (true) {
				const { value, done } = await reader.read();
				if (done) break;
				buf += decoder.decode(value, { stream: true });
				buf = parseSSE(buf, (event, data) => {
					if (event === 'tool') {
						if (data.status === 'running') running[data.name] = data.label || (data.name || '').replace(/_/g, ' ');
						else delete running[data.name];
						setStatus(running);
					} else if (event === 'chunk' && data.text) {
						md += data.text;
						streamer.update(md);
					} else if (event === 'error') {
						streamErr = data.error || 'plan failed';
					}
				});
			}
			if (streamErr) throw new Error(streamErr);
		} catch (e) {
			streamer.stop();
			showError('Could not plan your week — ' + e.message);
		} finally {
			setBusy(false);
			status.hidden = true;
			// The stored plan is the source of truth (review + proposed week).
			try { await load(); } catch (_) { }
		}
	}

	function startRun(body) {
		return stream(() => fetch('/api/coach/weekly-plan', {
			method: 'POST',
			headers: { 'Content-Type': 'application/json', 'Accept': 'text/event-stream' },
			body: JSON.stringify(body),
		}));
	}

	runBtn.addEventListener('click', () => {
		if (busy) return;
		const n = notes.value.trim();
		notes.value = '';
		startRun({ notes: n });
	});

	reviseBtn.addEventListener('click', () => {
		if (busy || !current) return;
		const n = notes.value.trim();
		if (!n) {
			showError('Say what to change first — e.g. “move the long ride to Sunday”.');
			notes.focus();
			return;
		}
		notes.value = '';
		startRun({ notes: n, revise_id: current.id });
	});

	// Enter revises the plan on screen when there is one, else plans fresh.
	notes.addEventListener('keydown', (e) => {
		if (e.key !== 'Enter') return;
		e.preventDefault();
		(reviseBtn.hidden ? runBtn : reviseBtn).click();
	});

	acceptBtn.addEventListener('click', async () => {
		if (busy || !current) return;
		setBusy(true);
		showError('');
		try {
			await api('/api/coach/weekly-plan/' + encodeURIComponent(current.id) + '/accept', { method: 'POST' });
			location.reload(); // show the new workouts on the grid
		} catch (e) {
			showError('Could not add the week — ' + e.message);
			setBusy(false);
		}
	});

	discardBtn.addEventListener('click', async () => {
		if (busy || !current) return;
		setBusy(true);
		showError('');
		try {
			await api('/api/coach/weekly-plan/' + encodeURIComponent(current.id) + '/discard', { method: 'POST' });
			await load();
		} catch (e) {
			showError('Could not discard — ' + e.message);
		} finally {
			setBusy(false);
		}
	});

	auto.addEventListener('change', async () => {
		const want = auto.checked;
		showError('');
		try {
			renderSchedule(await api('/api/coach/weekly-plan/schedule', {
				method: 'PUT',
				headers: { 'Content-Type': 'application/json' },
				body: JSON.stringify({ enabled: want }),
			}));
		} catch (e) {
			auto.checked = !want;
			showError('Could not change the auto-plan setting — ' + e.message);
		}
	});

	// Re-attach to a run that is already going: one started before navigating
	// here, or the Sunday-night auto-plan in progress.
	async function resumeIfRunning() {
		try {
			const res = await fetch('/api/coach/weekly-plan/active');
			if (!res.ok || res.status === 204) return;
			const env = await res.json();
			const j = env.data || env;
			if (j && j.id && !j.done) {
				await stream(() => fetch('/api/coach/weekly-plan/runs/' + encodeURIComponent(j.id) + '/events', {
					headers: { 'Accept': 'text/event-stream' },
				}));
			}
		} catch (_) { }
	}

	load()
		.catch((e) => showError('Could not load your weekly plan — ' + e.message))
		.then(resumeIfRunning);
})();
