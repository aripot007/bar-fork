<script lang="ts">
	import { goto } from '$app/navigation';
	import { accountsApi, authApi } from '$lib/requests/requests';
	import type { Account } from '$lib/api';
	import { onMount } from 'svelte';
	import { store } from '$lib/store/store';
	import Profile from '$lib/components/client/profile.svelte';
	import 'iconify-icon';
	import Autodisconnect from '$lib/components/random/autodisconnect.svelte';

	let account: Account | undefined = undefined;

	onMount(() => {
		accountsApi()
			.getAccount({
				withCredentials: true
			})
			.then((res) => {
				account = res.data.account;
				store.set({ account });
			})
			.catch(() => {
				goto('/client');
			});
	});

	function changeWantsToStaff() {
		accountsApi()
			.toggleAccountWantsToStaff({
				withCredentials: true
			})
			.then((res) => {
				if (account) account.wants_to_staff = res.data.wants_to_staff;
			});
	}
</script>

{#if account !== undefined}
	<div
		id="main"
		class="absolute top-0 l() => eft-0 w-screen h-screen"
		style="background-color:#393E46"
	>
		<div class="w-full p-4 flex justify-between" style="background-color:#222831">
			<!-- left div -->
			<Profile
				{account}
				logout={() => {
					goto('/auth');
				}}
			/>
			<!-- Good looking "wants to staff" toggle -->
			<label for="toggle" class="hidden sm:flex flex-col items-center cursor-pointer gap-3">
				<span class="text-white text-lg font-bold">Tu veux staffer ?</span>
				<div class="relative">
					<input type="checkbox" id="toggle" class="sr-only" on:click={changeWantsToStaff} />
					<div
						class="w-10 h-4 {account.wants_to_staff
							? 'bg-green-400'
							: 'bg-gray-400'} rounded-full shadow-inner"
					/>
					<div
						class="dot absolute w-6 h-6 bg-white rounded-full shadow -left-1 -top-1 transition {account.wants_to_staff
							? 'translate-x-[100%]'
							: 'translate-x-0'}"
					/>
				</div>
			</label>
			<!-- Good looking "order" button -->
			<button
				class="flex items-center space-x-2 px-4 py-2 rounded-lg bg-green-500 hover:bg-green-600 transition-colors duration-300 animate-pulse"
				on:click={() => {
					goto('/client/commande');
				}}
			>
				<iconify-icon class="text-white text-2xl" icon="mingcute:fork-fill" />
				<span class="text-white text-lg font-bold">Commander</span>
			</button>
		</div>

		<slot />
	</div>
{/if}
