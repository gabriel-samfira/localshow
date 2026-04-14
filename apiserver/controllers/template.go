package controllers

var tpl = `<!DOCTYPE html>
<html lang="en">
<head>
	<meta charset="utf-8">
	<meta name="viewport" content="width=device-width, initial-scale=1">
	<title>Localshow - SSH Brute Force Statistics</title>
	<script src="https://cdn.jsdelivr.net/npm/chart.js@4.4.8/dist/chart.umd.min.js"></script>
	<style>
		*,*::before,*::after{box-sizing:border-box;margin:0;padding:0}
		:root{
			--bg:#0f1117;
			--surface:#1a1d27;
			--border:#2a2d3a;
			--text:#e1e4ed;
			--text-muted:#8b8fa3;
			--accent:#6366f1;
			--accent-muted:rgba(99,102,241,.15);
			--red:#ef4444;--red-bg:rgba(239,68,68,.15);
			--amber:#f59e0b;--amber-bg:rgba(245,158,11,.15);
			--emerald:#10b981;--emerald-bg:rgba(16,185,129,.15);
			--blue:#3b82f6;--blue-bg:rgba(59,130,246,.15);
			--radius:12px;
		}
		body{
			font-family:-apple-system,BlinkMacSystemFont,'Segoe UI',Roboto,'Helvetica Neue',Arial,sans-serif;
			background:var(--bg);color:var(--text);
			line-height:1.6;min-height:100vh;
		}
		.container{max-width:1280px;margin:0 auto;padding:2rem 1.5rem}
		header{text-align:center;margin-bottom:2.5rem}
		header h1{font-size:1.75rem;font-weight:700;letter-spacing:-.02em}
		header p{color:var(--text-muted);font-size:.875rem;margin-top:.25rem}
		.stats-row{
			display:grid;grid-template-columns:repeat(auto-fit,minmax(180px,1fr));
			gap:1rem;margin-bottom:2rem;
		}
		.stat-card{
			background:var(--surface);border:1px solid var(--border);
			border-radius:var(--radius);padding:1.25rem;text-align:center;
		}
		.stat-card .value{font-size:1.75rem;font-weight:700;line-height:1.2}
		.stat-card .label{font-size:.75rem;color:var(--text-muted);text-transform:uppercase;letter-spacing:.05em;margin-top:.25rem}
		.stat-card.red .value{color:var(--red)}
		.stat-card.amber .value{color:var(--amber)}
		.stat-card.emerald .value{color:var(--emerald)}
		.stat-card.blue .value{color:var(--blue)}
		.grid{
			display:grid;
			grid-template-columns:repeat(2,1fr);
			gap:1.5rem;
		}
		@media(max-width:860px){.grid{grid-template-columns:1fr}}
		.card{
			background:var(--surface);border:1px solid var(--border);
			border-radius:var(--radius);padding:1.5rem;
			display:flex;flex-direction:column;
		}
		.card-title{
			font-size:.875rem;font-weight:600;color:var(--text-muted);
			text-transform:uppercase;letter-spacing:.04em;
			margin-bottom:1rem;padding-bottom:.75rem;
			border-bottom:1px solid var(--border);
		}
		.card-body{position:relative;flex:1;min-height:280px}
		.span-2{grid-column:1/-1}
		footer{text-align:center;padding:2rem 0 1rem;color:var(--text-muted);font-size:.75rem}
	</style>
</head>
<body>
	<div class="container">
		<header>
			<h1>SSH Brute Force Statistics</h1>
			<p>Data refreshes every 5 minutes</p>
		</header>

		<div class="stats-row">
			<div class="stat-card red">
				<div class="value" id="totalCountries">-</div>
				<div class="label">Countries</div>
			</div>
			<div class="stat-card amber">
				<div class="value" id="totalPasswords">-</div>
				<div class="label">Unique Passwords</div>
			</div>
			<div class="stat-card emerald">
				<div class="value" id="totalUsers">-</div>
				<div class="label">Unique Usernames</div>
			</div>
			<div class="stat-card blue">
				<div class="value" id="totalAttempts">-</div>
				<div class="label">Attempts (30 days)</div>
			</div>
		</div>

		<div class="grid">
			<div class="card">
				<div class="card-title">Top Countries by Unique IPs</div>
				<div class="card-body"><canvas id="countries"></canvas></div>
			</div>
			<div class="card">
				<div class="card-title">Most Used Passwords</div>
				<div class="card-body"><canvas id="passwords"></canvas></div>
			</div>
			<div class="card">
				<div class="card-title">Most Used Usernames</div>
				<div class="card-body"><canvas id="users"></canvas></div>
			</div>
			<div class="card span-2">
				<div class="card-title">Login Attempts (Past 30 Days)</div>
				<div class="card-body"><canvas id="authAttempts"></canvas></div>
			</div>
		</div>

		<footer>Localshow</footer>
	</div>

	<script>
	(function(){
		Chart.defaults.color = '#8b8fa3';
		Chart.defaults.borderColor = 'rgba(42,45,58,.6)';
		Chart.defaults.font.family = '-apple-system,BlinkMacSystemFont,"Segoe UI",Roboto,sans-serif';

		var palette = [
			'#6366f1','#3b82f6','#10b981','#f59e0b','#ef4444',
			'#8b5cf6','#06b6d4','#ec4899','#14b8a6','#f97316'
		];
		var paletteBg = palette.map(function(c){ return c + '26'; });

		function makeBar(id, labels, data, label) {
			return new Chart(document.getElementById(id), {
				type: 'bar',
				data: {
					labels: labels,
					datasets: [{
						label: label,
						data: data,
						backgroundColor: paletteBg,
						borderColor: palette,
						borderWidth: 1,
						borderRadius: 4,
						maxBarThickness: 48
					}]
				},
				options: {
					responsive: true,
					maintainAspectRatio: false,
					indexAxis: 'y',
					plugins: { legend: { display: false } },
					scales: {
						x: { grid: { display: false }, ticks: { precision: 0 } },
						y: { grid: { display: false } }
					}
				}
			});
		}

		function makeLine(id, labels, data, label) {
			return new Chart(document.getElementById(id), {
				type: 'line',
				data: {
					labels: labels,
					datasets: [{
						label: label,
						data: data,
						borderColor: '#6366f1',
						backgroundColor: 'rgba(99,102,241,.1)',
						borderWidth: 2,
						fill: true,
						tension: .3,
						pointRadius: 3,
						pointBackgroundColor: '#6366f1',
						pointBorderColor: '#1a1d27',
						pointBorderWidth: 2
					}]
				},
				options: {
					responsive: true,
					maintainAspectRatio: false,
					plugins: { legend: { display: false } },
					scales: {
						x: { grid: { display: false } },
						y: { grid: { color: 'rgba(42,45,58,.4)' }, ticks: { precision: 0 }, beginAtZero: true }
					}
				}
			});
		}

		function sum(arr){ var s=0; for(var i=0;i<arr.length;i++) s+=arr[i]; return s; }
		function fmt(n){ return n.toLocaleString(); }

		var countriesData = {{ .Countries.Data }};
		var passwordsData = {{ .Passwords.Data }};
		var usersData     = {{ .Users.Data }};
		var attemptsData  = {{ .AuthAttempts.Data }};

		document.getElementById('totalCountries').textContent  = fmt({{ .Countries.Labels }}.length);
		document.getElementById('totalPasswords').textContent  = fmt({{ .Passwords.Labels }}.length);
		document.getElementById('totalUsers').textContent      = fmt({{ .Users.Labels }}.length);
		document.getElementById('totalAttempts').textContent   = fmt(sum(attemptsData));

		makeBar('countries', {{ .Countries.Labels }},  countriesData, 'Unique IPs');
		makeBar('passwords', {{ .Passwords.Labels }},  passwordsData, 'Occurrences');
		makeBar('users',     {{ .Users.Labels }},      usersData,     'Occurrences');
		makeLine('authAttempts', {{ .AuthAttempts.Labels }}, attemptsData, 'Attempts');
	})();
	</script>
</body>
</html>
`
