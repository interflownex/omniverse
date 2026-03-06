import 'dart:convert';

import 'package:flutter/material.dart';
import 'package:http/http.dart' as http;

const Color deepVoidBlue = Color(0xFF050810);
const Color neonMint = Color(0xFF00FF94);
const Color electricTeal = Color(0xFF00D4FF);

const String defaultUserId = 'anderson';
const String apiBaseUrl = String.fromEnvironment(
  'NEXORA_API_BASE_URL',
  defaultValue: 'http://localhost',
);

void main() {
  runApp(const NexoraSuperApp());
}

class NexoraSuperApp extends StatelessWidget {
  const NexoraSuperApp({super.key});

  @override
  Widget build(BuildContext context) {
    final theme = ThemeData(
      brightness: Brightness.dark,
      scaffoldBackgroundColor: deepVoidBlue,
      colorScheme: const ColorScheme.dark(
        primary: neonMint,
        secondary: electricTeal,
        surface: Color(0xFF0B1320),
      ),
      appBarTheme: const AppBarTheme(
        backgroundColor: deepVoidBlue,
        foregroundColor: Colors.white,
      ),
      cardTheme: const CardThemeData(
        color: Color(0xFF0A1220),
        elevation: 0,
        margin: EdgeInsets.symmetric(vertical: 8),
      ),
      useMaterial3: true,
    );

    return MaterialApp(
      title: 'Nexora Super App',
      debugShowCheckedModeBanner: false,
      theme: theme,
      home: const SuperAppShell(),
    );
  }
}

enum SuperAppTab { pay, social, move, chat, menu }

class SuperAppShell extends StatefulWidget {
  const SuperAppShell({super.key});

  @override
  State<SuperAppShell> createState() => _SuperAppShellState();
}

class _SuperAppShellState extends State<SuperAppShell> {
  final ApiClient _api = ApiClient(baseUrl: apiBaseUrl);
  SuperAppTab _selected = SuperAppTab.pay;

  @override
  Widget build(BuildContext context) {
    final screens = <Widget>[
      PayScreen(api: _api, userId: defaultUserId),
      SocialScreen(api: _api, userId: defaultUserId),
      MoveScreen(api: _api, userId: defaultUserId),
      ChatScreen(api: _api),
      MenuScreen(api: _api, userId: defaultUserId),
    ];

    final items = <({IconData icon, String label})>[
      (icon: Icons.account_balance_wallet_outlined, label: 'Pay'),
      (icon: Icons.ondemand_video_outlined, label: 'Social'),
      (icon: Icons.directions_car_outlined, label: 'Move'),
      (icon: Icons.chat_bubble_outline, label: 'Chat'),
      (icon: Icons.grid_view_rounded, label: 'Menu'),
    ];

    return LayoutBuilder(
      builder: (context, constraints) {
        final desktop = constraints.maxWidth >= 980;
        if (!desktop) {
          return Scaffold(
            body: SafeArea(child: screens[_selected.index]),
            bottomNavigationBar: NavigationBar(
              selectedIndex: _selected.index,
              onDestinationSelected: (value) {
                setState(() => _selected = SuperAppTab.values[value]);
              },
              indicatorColor: neonMint.withOpacity(0.20),
              destinations: items
                  .map(
                    (it) => NavigationDestination(
                      icon: Icon(it.icon),
                      label: it.label,
                    ),
                  )
                  .toList(),
            ),
          );
        }

        return Scaffold(
          body: SafeArea(
            child: Row(
              children: [
                NavigationRail(
                  selectedIndex: _selected.index,
                  onDestinationSelected: (value) {
                    setState(() => _selected = SuperAppTab.values[value]);
                  },
                  backgroundColor: const Color(0xFF060D18),
                  indicatorColor: neonMint.withOpacity(0.20),
                  selectedIconTheme: const IconThemeData(color: neonMint),
                  selectedLabelTextStyle: const TextStyle(color: neonMint),
                  destinations: items
                      .map(
                        (it) => NavigationRailDestination(
                          icon: Icon(it.icon),
                          label: Text(it.label),
                        ),
                      )
                      .toList(),
                ),
                const VerticalDivider(width: 1),
                Expanded(child: screens[_selected.index]),
              ],
            ),
          ),
        );
      },
    );
  }
}

class PageFrame extends StatelessWidget {
  const PageFrame({
    super.key,
    required this.title,
    required this.subtitle,
    required this.child,
  });

  final String title;
  final String subtitle;
  final Widget child;

  @override
  Widget build(BuildContext context) {
    return Container(
      decoration: const BoxDecoration(
        gradient: LinearGradient(
          colors: [Color(0xFF050810), Color(0xFF061527)],
          begin: Alignment.topCenter,
          end: Alignment.bottomCenter,
        ),
      ),
      child: ListView(
        padding: const EdgeInsets.fromLTRB(16, 16, 16, 24),
        children: [
          Text(
            title,
            style: const TextStyle(
              fontSize: 26,
              fontWeight: FontWeight.w700,
              color: Colors.white,
            ),
          ),
          const SizedBox(height: 6),
          Text(
            subtitle,
            style: const TextStyle(color: electricTeal, fontSize: 14),
          ),
          const SizedBox(height: 16),
          child,
        ],
      ),
    );
  }
}

class PayScreen extends StatefulWidget {
  const PayScreen({super.key, required this.api, required this.userId});

  final ApiClient api;
  final String userId;

  @override
  State<PayScreen> createState() => _PayScreenState();
}

class _PayScreenState extends State<PayScreen> {
  late Future<Map<String, dynamic>> _balanceFuture;

  @override
  void initState() {
    super.initState();
    _balanceFuture = widget.api.getWalletBalance(widget.userId);
  }

  Future<void> _generateReceipt() async {
    final messenger = ScaffoldMessenger.of(context);
    final payload = {
      'source': 'super-app-pay',
      'order_id': 'pay-${DateTime.now().millisecondsSinceEpoch}',
      'buyer_user_id': widget.userId,
      'seller_user_id': 'nexora-finance',
      'currency': 'BRL',
      'gross_cents': 10990,
      'fee_cents': 95,
      'net_cents': 10895,
      'description': 'Recarga de carteira no Super App',
    };

    try {
      final response = await widget.api.generateReceipt(payload);
      messenger.showSnackBar(
        SnackBar(content: Text('Comprovante enfileirado: ${response['document_id'] ?? 'ok'}')),
      );
    } catch (err) {
      messenger.showSnackBar(SnackBar(content: Text('Falha ao gerar comprovante: $err')));
    }
  }

  @override
  Widget build(BuildContext context) {
    return PageFrame(
      title: 'Pay',
      subtitle: 'Carteira digital, histórico e comprovantes PDF',
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          FutureBuilder<Map<String, dynamic>>(
            future: _balanceFuture,
            builder: (context, snapshot) {
              if (snapshot.connectionState == ConnectionState.waiting) {
                return const LinearProgressIndicator(color: neonMint);
              }

              if (snapshot.hasError) {
                return _InfoCard(
                  title: 'Saldo indisponível',
                  text: 'Não foi possível carregar a carteira. ${snapshot.error}',
                );
              }

              final balance = snapshot.data ?? {};
              final brlBalance = '${balance['brl_balance'] ?? '0.00'}';
              final nexBalance = '${balance['nex_balance'] ?? '0.000000'}';
              return _InfoCard(
                title: 'Carteira de $defaultUserId',
                text: 'BRL R\$ $brlBalance\nNEX $nexBalance',
                borderColor: neonMint,
              );
            },
          ),
          const SizedBox(height: 10),
          Card(
            child: Padding(
              padding: const EdgeInsets.all(14),
              child: Column(
                crossAxisAlignment: CrossAxisAlignment.start,
                children: [
                  const Text('Ações financeiras', style: TextStyle(fontWeight: FontWeight.w700)),
                  const SizedBox(height: 10),
                  FilledButton.icon(
                    onPressed: _generateReceipt,
                    icon: const Text('📄'),
                    label: const Text('Gerar Comprovante PDF'),
                    style: FilledButton.styleFrom(
                      backgroundColor: neonMint,
                      foregroundColor: deepVoidBlue,
                    ),
                  ),
                ],
              ),
            ),
          ),
          const SizedBox(height: 10),
          PackageTrackingCard(api: widget.api),
        ],
      ),
    );
  }
}

class SocialScreen extends StatefulWidget {
  const SocialScreen({super.key, required this.api, required this.userId});

  final ApiClient api;
  final String userId;

  @override
  State<SocialScreen> createState() => _SocialScreenState();
}

class _SocialScreenState extends State<SocialScreen> {
  final List<SocialVideo> _videos = [];
  String? _cursor;
  bool _loading = false;
  bool _hasMore = true;
  String? _error;

  @override
  void initState() {
    super.initState();
    _loadNextPage();
  }

  Future<void> _loadNextPage() async {
    if (_loading || !_hasMore) return;
    setState(() {
      _loading = true;
      _error = null;
    });
    try {
      final response = await widget.api.fetchVideos(cursor: _cursor, limit: 12);
      setState(() {
        _videos.addAll(response.items);
        _cursor = response.nextCursor;
        _hasMore = response.hasMore;
      });
    } catch (err) {
      setState(() => _error = '$err');
    } finally {
      if (mounted) {
        setState(() => _loading = false);
      }
    }
  }

  Future<void> _oneClickBuy(SocialVideo video) async {
    final messenger = ScaffoldMessenger.of(context);
    final category = video.tags.isNotEmpty ? video.tags.last : 'trending';

    try {
      final suggestion = await widget.api.pickSuggestionForCategory(category);
      if (suggestion == null) {
        messenger.showSnackBar(const SnackBar(content: Text('Nenhum produto sugerido disponível.')));
        return;
      }

      final result = await widget.api.oneClickPurchase(
        videoId: video.id,
        productId: suggestion.productId,
        buyerUserId: widget.userId,
        supplierUserId: 'dropship-${suggestion.source}',
      );
      messenger.showSnackBar(
        SnackBar(content: Text('Compra enviada: ${result['order_id'] ?? result['status'] ?? 'ok'}')),
      );
    } catch (err) {
      messenger.showSnackBar(SnackBar(content: Text('Falha na compra: $err')));
    }
  }

  @override
  Widget build(BuildContext context) {
    if (_videos.isEmpty && _loading) {
      return const Center(child: CircularProgressIndicator(color: neonMint));
    }

    if (_error != null && _videos.isEmpty) {
      return Center(
        child: _InfoCard(
          title: 'Feed indisponível',
          text: _error!,
        ),
      );
    }

    return PageView.builder(
      scrollDirection: Axis.vertical,
      itemCount: _videos.length + (_hasMore ? 1 : 0),
      onPageChanged: (index) {
        if (index >= _videos.length - 2) {
          _loadNextPage();
        }
      },
      itemBuilder: (context, index) {
        if (index >= _videos.length) {
          return const Center(child: CircularProgressIndicator(color: neonMint));
        }
        final video = _videos[index];
        return Container(
          decoration: BoxDecoration(
            gradient: LinearGradient(
              colors: [
                deepVoidBlue,
                Color.lerp(deepVoidBlue, electricTeal, 0.18) ?? deepVoidBlue,
              ],
              begin: Alignment.topCenter,
              end: Alignment.bottomCenter,
            ),
          ),
          padding: const EdgeInsets.all(16),
          child: Column(
            crossAxisAlignment: CrossAxisAlignment.start,
            children: [
              const SizedBox(height: 24),
              Text(
                'Social',
                style: Theme.of(context).textTheme.titleLarge?.copyWith(color: Colors.white),
              ),
              const SizedBox(height: 8),
              Expanded(
                child: Card(
                  child: Padding(
                    padding: const EdgeInsets.all(16),
                    child: Column(
                      crossAxisAlignment: CrossAxisAlignment.start,
                      children: [
                        Text(video.title, style: const TextStyle(fontSize: 20, fontWeight: FontWeight.bold)),
                        const SizedBox(height: 8),
                        Text(video.description, style: const TextStyle(color: Colors.white70)),
                        const SizedBox(height: 14),
                        Wrap(
                          spacing: 6,
                          runSpacing: 6,
                          children: video.tags
                              .take(4)
                              .map(
                                (tag) => Chip(
                                  label: Text('#$tag'),
                                  backgroundColor: electricTeal.withOpacity(0.22),
                                ),
                              )
                              .toList(),
                        ),
                        const Spacer(),
                        Row(
                          children: [
                            Icon(Icons.remove_red_eye_outlined, color: electricTeal.withOpacity(0.9)),
                            const SizedBox(width: 6),
                            Text('${video.views} views'),
                            const SizedBox(width: 18),
                            Icon(Icons.favorite_border, color: neonMint.withOpacity(0.9)),
                            const SizedBox(width: 6),
                            Text('${video.likes} likes'),
                          ],
                        ),
                        const SizedBox(height: 10),
                        FilledButton.icon(
                          onPressed: () => _oneClickBuy(video),
                          icon: const Icon(Icons.shopping_cart_checkout_rounded),
                          label: const Text('Compra Nativa'),
                          style: FilledButton.styleFrom(
                            backgroundColor: neonMint,
                            foregroundColor: deepVoidBlue,
                          ),
                        ),
                      ],
                    ),
                  ),
                ),
              ),
            ],
          ),
        );
      },
    );
  }
}

class MoveScreen extends StatelessWidget {
  const MoveScreen({super.key, required this.api, required this.userId});

  final ApiClient api;
  final String userId;

  Future<void> _generateRideReceipt(BuildContext context) async {
    final messenger = ScaffoldMessenger.of(context);
    try {
      final response = await api.generateReceipt({
        'source': 'nexora-move',
        'order_id': 'ride-${DateTime.now().millisecondsSinceEpoch}',
        'buyer_user_id': userId,
        'seller_user_id': 'driver-betim-01',
        'currency': 'BRL',
        'gross_cents': 3990,
        'fee_cents': 399,
        'net_cents': 3591,
        'description': 'Corrida urbana tarifa fixa Nexora Move',
      });
      messenger.showSnackBar(SnackBar(content: Text('Comprovante gerado: ${response['document_id'] ?? 'ok'}')));
    } catch (err) {
      messenger.showSnackBar(SnackBar(content: Text('Erro ao gerar PDF: $err')));
    }
  }

  @override
  Widget build(BuildContext context) {
    return PageFrame(
      title: 'Move',
      subtitle: 'Corridas urbanas com tarifa fixa e status em tempo real',
      child: Column(
        children: [
          const _InfoCard(
            title: 'Corrida ativa',
            text: 'Motorista: Carla S.\nOrigem: Centro Betim\nDestino: Citrolandia\nETA: 6 min',
            borderColor: electricTeal,
          ),
          const SizedBox(height: 10),
          Card(
            child: Padding(
              padding: const EdgeInsets.all(14),
              child: Column(
                crossAxisAlignment: CrossAxisAlignment.start,
                children: [
                  const Text('Financeiro da corrida', style: TextStyle(fontWeight: FontWeight.w700)),
                  const SizedBox(height: 8),
                  const Text('Total: R\$ 39,90 | Taxa Nexora: 10%'),
                  const SizedBox(height: 10),
                  FilledButton.icon(
                    onPressed: () => _generateRideReceipt(context),
                    icon: const Text('📄'),
                    label: const Text('Gerar Comprovante PDF'),
                    style: FilledButton.styleFrom(
                      backgroundColor: neonMint,
                      foregroundColor: deepVoidBlue,
                    ),
                  ),
                ],
              ),
            ),
          ),
        ],
      ),
    );
  }
}

class ChatScreen extends StatefulWidget {
  const ChatScreen({super.key, required this.api});

  final ApiClient api;

  @override
  State<ChatScreen> createState() => _ChatScreenState();
}

class _ChatScreenState extends State<ChatScreen> {
  bool professionalPersona = false;

  @override
  Widget build(BuildContext context) {
    return PageFrame(
      title: 'Chat',
      subtitle: 'Mensageiro instantâneo com alternância de persona',
      child: Column(
        children: [
          Card(
            child: Padding(
              padding: const EdgeInsets.all(14),
              child: Row(
                children: [
                  Expanded(
                    child: Text(
                      professionalPersona ? 'Persona Profissional' : 'Persona Pessoal',
                      style: const TextStyle(fontSize: 16, fontWeight: FontWeight.w700),
                    ),
                  ),
                  Switch(
                    value: professionalPersona,
                    onChanged: (value) => setState(() => professionalPersona = value),
                    activeColor: neonMint,
                  ),
                ],
              ),
            ),
          ),
          const SizedBox(height: 8),
          _InfoCard(
            title: professionalPersona ? 'Canal Trabalho' : 'Canal Família',
            text: professionalPersona
                ? 'Mensagens de negócio serão filtradas pelo Burnout Shield.'
                : 'Conversa pessoal ativa com prioridade alta.',
          ),
        ],
      ),
    );
  }
}

class MenuScreen extends StatelessWidget {
  const MenuScreen({super.key, required this.api, required this.userId});

  final ApiClient api;
  final String userId;

  @override
  Widget build(BuildContext context) {
    return PageFrame(
      title: 'Menu',
      subtitle: 'Acesso rápido aos módulos do ecossistema Nexora',
      child: Column(
        children: [
          PackageTrackingCard(api: api),
          const SizedBox(height: 10),
          const _InfoCard(
            title: 'Atalhos',
            text:
                'Life • Trainer • School & Job • Stock • Place • Food • Business • Plug • UP\nTudo conectado no painel admin.',
            borderColor: electricTeal,
          ),
          const SizedBox(height: 10),
          _InfoCard(
            title: 'Usuário ativo',
            text: 'ID: $userId\nModo: produção local',
          ),
        ],
      ),
    );
  }
}

class PackageTrackingCard extends StatefulWidget {
  const PackageTrackingCard({super.key, required this.api});

  final ApiClient api;

  @override
  State<PackageTrackingCard> createState() => _PackageTrackingCardState();
}

class _PackageTrackingCardState extends State<PackageTrackingCard> {
  final TextEditingController _controller = TextEditingController(text: 'NXR123456BR');
  bool _loading = false;
  String _error = '';
  List<Map<String, dynamic>> _items = const [];

  Future<void> _track() async {
    final code = _controller.text.trim();
    if (code.isEmpty) {
      setState(() => _error = 'Informe o código de rastreio.');
      return;
    }

    setState(() {
      _loading = true;
      _error = '';
    });

    try {
      final snapshots = await widget.api.trackPackage(code);
      setState(() => _items = snapshots);
    } catch (err) {
      setState(() => _error = '$err');
    } finally {
      if (mounted) {
        setState(() => _loading = false);
      }
    }
  }

  @override
  Widget build(BuildContext context) {
    return Card(
      child: Padding(
        padding: const EdgeInsets.all(14),
        child: Column(
          crossAxisAlignment: CrossAxisAlignment.start,
          children: [
            const Text(
              'Rastreio de Pacote (injetado na tela do usuário)',
              style: TextStyle(fontWeight: FontWeight.w700),
            ),
            const SizedBox(height: 10),
            Row(
              children: [
                Expanded(
                  child: TextField(
                    controller: _controller,
                    decoration: const InputDecoration(
                      labelText: 'Código de rastreio',
                      border: OutlineInputBorder(),
                    ),
                  ),
                ),
                const SizedBox(width: 8),
                FilledButton(
                  onPressed: _loading ? null : _track,
                  style: FilledButton.styleFrom(
                    backgroundColor: electricTeal,
                    foregroundColor: deepVoidBlue,
                  ),
                  child: _loading
                      ? const SizedBox(
                          width: 16,
                          height: 16,
                          child: CircularProgressIndicator(strokeWidth: 2),
                        )
                      : const Text('Rastrear'),
                ),
              ],
            ),
            if (_error.isNotEmpty) ...[
              const SizedBox(height: 8),
              Text(_error, style: const TextStyle(color: Colors.redAccent)),
            ],
            if (_items.isNotEmpty) ...[
              const SizedBox(height: 10),
              ..._items.map(
                (item) => Padding(
                  padding: const EdgeInsets.only(bottom: 6),
                  child: Text(
                    '${item['carrier']}: ${item['status']} (${item['updated_at'] ?? '-'})',
                    style: const TextStyle(fontSize: 12),
                  ),
                ),
              ),
            ],
          ],
        ),
      ),
    );
  }
}

class _InfoCard extends StatelessWidget {
  const _InfoCard({
    required this.title,
    required this.text,
    this.borderColor,
  });

  final String title;
  final String text;
  final Color? borderColor;

  @override
  Widget build(BuildContext context) {
    return Card(
      shape: RoundedRectangleBorder(
        borderRadius: BorderRadius.circular(12),
        side: BorderSide(color: borderColor ?? Colors.white12),
      ),
      child: Padding(
        padding: const EdgeInsets.all(14),
        child: Column(
          crossAxisAlignment: CrossAxisAlignment.start,
          children: [
            Text(title, style: const TextStyle(fontWeight: FontWeight.w700)),
            const SizedBox(height: 8),
            Text(text, style: const TextStyle(height: 1.35)),
          ],
        ),
      ),
    );
  }
}

class ApiClient {
  ApiClient({required this.baseUrl});

  final String baseUrl;
  final http.Client _http = http.Client();

  Uri _u(String path, [Map<String, String>? query]) {
    final safePath = path.startsWith('/') ? path : '/$path';
    return Uri.parse('$baseUrl$safePath').replace(queryParameters: query);
  }

  Future<Map<String, dynamic>> _jsonGet(String path, {Map<String, String>? query}) async {
    final response = await _http.get(_u(path, query), headers: {'accept': 'application/json'});
    if (response.statusCode < 200 || response.statusCode > 299) {
      throw Exception('GET $path failed (${response.statusCode})');
    }
    return (jsonDecode(response.body) as Map).cast<String, dynamic>();
  }

  Future<Map<String, dynamic>> _jsonPost(
    String path,
    Map<String, dynamic> payload, {
    Map<String, String>? headers,
  }) async {
    final finalHeaders = <String, String>{
      'accept': 'application/json',
      'content-type': 'application/json',
      ...?headers,
    };
    final response = await _http.post(_u(path), headers: finalHeaders, body: jsonEncode(payload));
    if (response.statusCode < 200 || response.statusCode > 299) {
      throw Exception('POST $path failed (${response.statusCode}): ${response.body}');
    }
    return (jsonDecode(response.body) as Map).cast<String, dynamic>();
  }

  Future<Map<String, dynamic>> getWalletBalance(String userId) {
    return _jsonGet('/pay/v1/wallets/$userId/balance');
  }

  Future<Map<String, dynamic>> generateReceipt(Map<String, dynamic> payload) {
    return _jsonPost(
      '/documents/v1/events/purchase',
      payload,
      headers: {'x-doc-engine-token': 'doc-engine-token'},
    );
  }

  Future<VideoPage> fetchVideos({String? cursor, int limit = 12}) async {
    final json = await _jsonGet('/social/v1/videos', query: {
      'limit': '$limit',
      if (cursor != null && cursor.isNotEmpty) 'cursor': cursor,
    });

    final rows = (json['data'] as List? ?? []).cast<dynamic>();
    final items = rows
        .map((row) => SocialVideo.fromJson((row as Map).cast<String, dynamic>()))
        .toList(growable: false);

    final nextCursor = (json['next_cursor'] ?? '').toString();
    final hasMore = nextCursor.isNotEmpty;
    return VideoPage(items: items, nextCursor: nextCursor.isEmpty ? null : nextCursor, hasMore: hasMore);
  }

  Future<StockProduct?> pickSuggestionForCategory(String category) async {
    final json = await _jsonGet('/stock/v1/products/suggestions', query: {
      'source': 'all',
      'category': category,
      'limit': '1',
    });
    final rows = (json['data'] as List? ?? []).cast<dynamic>();
    if (rows.isEmpty) return null;
    final row = (rows.first as Map).cast<String, dynamic>();
    return StockProduct.fromSuggestion(row, category: category);
  }

  Future<Map<String, dynamic>> oneClickPurchase({
    required String videoId,
    required String productId,
    required String buyerUserId,
    required String supplierUserId,
  }) {
    return _jsonPost('/stock/v1/payments/one-click', {
      'video_id': videoId,
      'product_id': productId,
      'buyer_user_id': buyerUserId,
      'supplier_user_id': supplierUserId,
      'quantity': 1,
      'currency': 'BRL',
    });
  }

  Future<List<Map<String, dynamic>>> trackPackage(String trackingCode) async {
    final json = await _jsonGet('/stock/v1/tracking/status-all', query: {'tracking_code': trackingCode});
    final rows = (json['data'] as List? ?? []).cast<dynamic>();
    return rows.map((row) => (row as Map).cast<String, dynamic>()).toList(growable: false);
  }
}

class VideoPage {
  const VideoPage({
    required this.items,
    required this.nextCursor,
    required this.hasMore,
  });

  final List<SocialVideo> items;
  final String? nextCursor;
  final bool hasMore;
}

class SocialVideo {
  SocialVideo({
    required this.id,
    required this.title,
    required this.description,
    required this.views,
    required this.likes,
    required this.tags,
  });

  final String id;
  final String title;
  final String description;
  final int views;
  final int likes;
  final List<String> tags;

  factory SocialVideo.fromJson(Map<String, dynamic> json) {
    final tags = (json['tags'] as List? ?? const []).map((e) => '$e').toList(growable: false);
    return SocialVideo(
      id: '${json['id'] ?? ''}',
      title: '${json['title'] ?? 'Sem título'}',
      description: '${json['description'] ?? ''}',
      views: int.tryParse('${json['views'] ?? 0}') ?? 0,
      likes: int.tryParse('${json['likes'] ?? 0}') ?? 0,
      tags: tags,
    );
  }
}

class StockProduct {
  StockProduct({
    required this.productId,
    required this.source,
  });

  final String productId;
  final String source;

  factory StockProduct.fromSuggestion(Map<String, dynamic> row, {required String category}) {
    final source = '${row['source'] ?? 'manual'}'.toLowerCase().trim();
    final external = '${row['external_id'] ?? 'item-001'}'.toLowerCase().trim();
    return StockProduct(
      productId: 'prd-${source.replaceAll(' ', '-')}-${external.replaceAll(' ', '-')}-${category.toLowerCase().trim()}',
      source: source,
    );
  }
}
