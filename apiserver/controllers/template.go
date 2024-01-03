package controllers

var tpl = `<!DOCTYPE html>
<html lang='en'>
	<head>
		<title>Localshow</title>
		<script src="https://cdn.jsdelivr.net/npm/chart.js@4.4.1/dist/chart.umd.min.js"></script>
	</head>
	<body>
		<div style="display: flex; flex-direction: column; align-items: center; justify-content: center; width: 100%; height: 100%;">
			<div style="width: 60%; text-align: center;">
				<h1>SSH server brute force statistics</h1>
            </div>
			<div style="width: 60%; height: 500px;">
				<canvas id="countries"></canvas>
			</div>
			<div style="width: 60%; height: 500px;">
				<canvas id="passwords"></canvas>
			</div>
			<div style="width: 60%; height: 500px;">
				<canvas id="users"></canvas>
			</div>
			<div style="width: 60%; height: 500px;">
				<canvas id="authAttempts"></canvas>
			</div>
		</div>

		<script>
			var countries = document.getElementById('countries').getContext('2d');
			var passwords = document.getElementById('passwords').getContext('2d');
			var users = document.getElementById('users').getContext('2d');
			var authAttempts = document.getElementById('authAttempts').getContext('2d');
			var countriesChart = new Chart(countries, {
				type: 'bar',
				data: {
					labels: {{ .Countries.Labels }},
					datasets: [{
						label: 'Top 10 countries by unique IP addresses',
						data: {{ .Countries.Data }},
						backgroundColor: [
							'rgba(255, 99, 132, 0.2)',
							'rgba(54, 162, 235, 0.2)',
							'rgba(255, 206, 86, 0.2)',
							'rgba(75, 192, 192, 0.2)',
							'rgba(153, 102, 255, 0.2)',
							'rgba(255, 159, 64, 0.2)',
							'rgba(255, 99, 132, 0.2)',
							'rgba(54, 162, 235, 0.2)',
							'rgba(255, 206, 86, 0.2)',
							'rgba(75, 192, 192, 0.2)'
						],
						borderColor: [
							'rgba(255, 99, 132, 1)',
							'rgba(54, 162, 235, 1)',
							'rgba(255, 206, 86, 1)',
							'rgba(75, 192, 192, 1)',
							'rgba(153, 102, 255, 1)',
							'rgba(255, 159, 64, 1)',
							'rgba(255, 99, 132, 1)',
							'rgba(54, 162, 235, 1)',
							'rgba(255, 206, 86, 1)',
							'rgba(75, 192, 192, 1)'
						],
						borderWidth: 1
					}]
				},
				options: {
					responsive: true,
					maintainAspectRatio: false,
					plugins: {
						legend: {
							position: 'top',
						},
						title: {
							display: false,
							text: 'Top 10 countries by unique IP addresses'
						}
					}
				}
			});
			var passwordsChart = new Chart(passwords, {
				type: 'bar',
				data: {
					labels: {{ .Passwords.Labels }},
					datasets: [{
						label: 'Top 10 most used passwords',
						data: {{ .Passwords.Data }},
						backgroundColor: [
							'rgba(255, 99, 132, 0.2)',
							'rgba(54, 162, 235, 0.2)',
							'rgba(255, 206, 86, 0.2)',
							'rgba(75, 192, 192, 0.2)',
							'rgba(153, 102, 255, 0.2)',
							'rgba(255, 159, 64, 0.2)',
							'rgba(255, 99, 132, 0.2)',
							'rgba(54, 162, 235, 0.2)',
							'rgba(255, 206, 86, 0.2)',
							'rgba(75, 192, 192, 0.2)'
						],
						borderColor: [
							'rgba(255, 99, 132, 1)',
							'rgba(54, 162, 235, 1)',
							'rgba(255, 206, 86, 1)',
							'rgba(75, 192, 192, 1)',
							'rgba(153, 102, 255, 1)',
							'rgba(255, 159, 64, 1)',
							'rgba(255, 99, 132, 1)',
							'rgba(54, 162, 235, 1)',
							'rgba(255, 206, 86, 1)',
							'rgba(75, 192, 192, 1)'
						],
						borderWidth: 1
					}]
				},
				options: {
					responsive: true,
					maintainAspectRatio: false,
					plugins: {
						legend: {
							position: 'top',
						},
						title: {
							display: false,
							text: 'Top 10 most used passwords'
						}
					}
				}
			});
			var usersChart = new Chart(users, {
				type: 'bar',
				data: {
					labels: {{ .Users.Labels }},
					datasets: [{
						label: 'Top 10 most used usernames',
						data: {{ .Users.Data }},
						backgroundColor: [
							'rgba(255, 99, 132, 0.2)',
							'rgba(54, 162, 235, 0.2)',
							'rgba(255, 206, 86, 0.2)',
							'rgba(75, 192, 192, 0.2)',
							'rgba(153, 102, 255, 0.2)',
							'rgba(255, 159, 64, 0.2)',
							'rgba(255, 99, 132, 0.2)',
							'rgba(54, 162, 235, 0.2)',
							'rgba(255, 206, 86, 0.2)',
							'rgba(75, 192, 192, 0.2)'
						],
						borderColor: [
							'rgba(255, 99, 132, 1)',
							'rgba(54, 162, 235, 1)',
							'rgba(255, 206, 86, 1)',
							'rgba(75, 192, 192, 1)',
							'rgba(153, 102, 255, 1)',
							'rgba(255, 159, 64, 1)',
							'rgba(255, 99, 132, 1)',
							'rgba(54, 162, 235, 1)',
							'rgba(255, 206, 86, 1)',
							'rgba(75, 192, 192, 1)'
						],
						borderWidth: 1
					}]
				},
				options: {
					responsive: true,
					maintainAspectRatio: false,
					plugins: {
						legend: {
							position: 'top',
						},
						title: {
							display: false,
							text: 'Top 10 most used usernames'
						}
					}
				}
			});
			var authAttemptsChart = new Chart(authAttempts, {
				type: 'line',
				data: {
					labels: {{ .AuthAttempts.Labels }},
					datasets: [{
						label: 'Auth attempts for the past 30 days',
						data: {{ .AuthAttempts.Data }},
						backgroundColor: [
							'rgba(255, 99, 132, 0.2)',
						],
						borderColor: [
							'rgba(255, 99, 132, 1)',
						],
						borderWidth: 1
					}]
				},
				options: {
					responsive: true,
					maintainAspectRatio: false,
					plugins: {
						legend: {
							position: 'top',
						},
						title: {
							display: false,
							text: 'Auth attempts for the past 30 days'
						}
					}
				}
			});
		</script>
	</body>
</html>
`
